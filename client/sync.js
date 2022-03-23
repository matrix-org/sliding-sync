/*
 * This file contains the main sliding sync code.
 * It defines the core sliding sync types and contains code for handling list deltas.
 * It doesn't concern itself with storing room data or updating the UI in any way, deferring
 * to room/lifecycle callbacks for that.
 */

import * as devtools from "./devtools.js";

// The default range to /always/ track on a list.
// When you scroll the list, new windows are added to the first element. E.g [[0,20], [37,45]]
// TODO: explain why
const DEFAULT_RANGES = [[0, 20]];

// We'll load these events for every room cell in each list.
// Note we do not need any room name information as the server calculates all this for us.
const REQUIRED_STATE_EVENTS_IN_LIST = [
    ["m.room.avatar", ""], // need the avatar
    ["m.room.tombstone", ""], // need to know if this room has been replaced
];
const REQUIRED_STATE_EVENTS_IN_ROOM = [
    ["m.room.avatar", ""], // need the avatar to display next to the room name
    ["m.room.tombstone", ""], // need to know if this room has been replaced
    ["m.room.topic", ""], // need the topic to display on the right-hand-side
];

// Lifecycle state when the /sync response has been fully processed and all room data callbacks
// have been invoked. Never contains an error.
export const LifecycleSyncComplete = 1;
// Lifecycle state when the /sync request has returned. May include an error if there was a problem
// talking to the server.
export const LifecycleSyncRequestFinished = 2;

/**
 * SlidingSyncConnection is a thin wrapper around fetch() which performs a sliding sync request.
 * The wrapper persists a small amount of extra data including the total number of tx/rx bytes,
 * as well as the abort controller for interrupting requests. One SlidingSyncConnection should be
 * made for the lifetime of the connection to ensure tx/rx bytes tally up across multiple requests.
 */
export class SlidingSyncConnection {
    constructor() {
        this.txBytes = 0;
        this.rxBytes = 0;
        this.abortController = new AbortController();
    }

    /**
     * Abort an active in-flight sync request if there is one currently. Else does nothing.
     */
    abort() {
        if (this.abortController) {
            this.abortController.abort();
        }
    }

    /**
     * Do a sliding sync request.
     * @param {string} accessToken The access token for the user.
     * @param {string} pos The ?pos= value
     * @param {object} reqBody The request JSON body to send.
     * @returns {object} The response JSON body
     */
    async doSyncRequest(accessToken, pos, reqBody) {
        this.abortController = new AbortController();
        const jsonBody = JSON.stringify(reqBody);
        let resp = await fetch(
            "/_matrix/client/v3/sync" + (pos ? "?pos=" + pos : ""),
            {
                signal: this.abortController.signal,
                method: "POST",
                headers: {
                    Authorization: "Bearer " + accessToken,
                    "Content-Type": "application/json",
                },
                body: jsonBody,
            }
        );

        let respBody = await resp.json();
        if (respBody.ops) {
            console.log(respBody);
        }
        // 0 TX / 0 RX (KB)
        this.txBytes += jsonBody.length;
        this.rxBytes += JSON.stringify(respBody).length;

        // TODO: Move?
        devtools.bandwidth(document.getElementById("txrx"), this);

        if (resp.status != 200) {
            throw new Error(
                "/sync returned HTTP " + resp.status + " " + respBody.error
            );
        }
        return respBody;
    }
}

/**
 * SlidingList represents a single list in sliding sync. The list can have filters, multiple sliding
 * windows, and maintains the index->room_id mapping.
 */
export class SlidingList {
    /**
     * Construct a new sliding list.
     * @param {string} name Human-readable name to display on the UI for this list.
     * @param {object} filters Optional. The sliding sync filters to apply e.g { is_dm: true }.
     */
    constructor(name, filters) {
        this.name = name;
        this.filters = filters || {};
        this.activeRanges = JSON.parse(JSON.stringify(DEFAULT_RANGES));
        // the constantly changing sliding window ranges. Not an array for performance reasons
        // E.g tracking ranges 0-99, 500-599, we don't want to have a 600 element array
        this.roomIndexToRoomId = {};
        // the total number of joined rooms according to the server, always >= len(roomIndexToRoomId)
        this.joinedCount = 0;
    }

    /**
     * Get the filters for this list.
     * @returns {object} A copy of the filters
     */
    getFilters() {
        // return a copy of the filters to ensure the caller cannot modify it without calling setFilters
        return Object.assign({}, this.filters);
    }

    /**
     * Modify the filters on this list. The filters provided are copied.
     * @param {object} filters The sliding sync filters to apply e.g { is_dm: true }.
     */
    setFilters(filters) {
        this.filters = Object.assign({}, filters);
        // if we are viewing a window at 100-120 and then we filter down to 5 total rooms,
        // we'll end up showing nothing. Therefore, if the filters change (e.g room name filter)
        // reset the range back to 0-20.
        this.activeRanges = JSON.parse(JSON.stringify(DEFAULT_RANGES));
        // Wipe the index to room ID map as the filters have changed which will invalidate these.
        this.roomIndexToRoomId = {};
    }
}

/**
 * SlidingSync is a high-level data structure which controls the majority of sliding sync.
 */
export class SlidingSync {
    /**
     * Create a new sliding sync instance
     * @param {[]SlidingList} lists The lists to use for sliding sync.
     * @param {SlidingSyncConnection} conn The connection to use for /sync calls.
     */
    constructor(lists, conn) {
        this.lists = lists;
        this.conn = conn;
        this.terminated = false;
        this.roomSubscription = "";
        this.roomDataCallbacks = [];
        this.lifecycleCallbacks = [];
    }

    /**
     * Listen for high-level room events on the sync connection
     * @param {function} callback The callback to invoke.
     */
    addRoomDataListener(callback) {
        this.roomDataCallbacks.push(callback);
    }

    /**
     * Listen for high-level lifecycle events on the sync connection
     * @param {function} callback The callback to invoke.
     */
    addLifecycleListener(callback) {
        this.lifecycleCallbacks.push(callback);
    }

    /**
     * Invoke all attached room data listeners.
     * @param {string} roomId The room which received some data.
     * @param {object} roomData The raw sliding sync response JSON.
     */
    _invokeRoomDataListeners(roomId, roomData) {
        this.roomDataCallbacks.forEach((callback) => {
            callback(roomId, roomData);
        });
    }

    /**
     * Invoke all attached lifecycle listeners.
     * @param {Number} state The Lifecycle state
     * @param {object} resp The raw sync response JSON
     * @param {Error?} err Any error that occurred when making the request e.g network errors.
     */
    _invokeLifecycleListeners(state, resp, err) {
        this.lifecycleCallbacks.forEach((callback) => {
            callback(state, resp, err);
        });
    }

    /**
     * Stop syncing with the server.
     */
    stop() {
        this.terminated = true;
        this.conn.abort();
    }

    /**
     * Start syncing with the server. Blocks until stopped.
     * @param {string} accessToken The access token to sync with.
     */
    async start(accessToken) {
        let currentPos;
        let currentSub = "";
        while (!this.terminated) {
            let resp;
            try {
                // these fields are always required
                let reqBody = {
                    lists: this.lists.map((al) => {
                        let l = {
                            ranges: al.activeRanges,
                            filters: al.getFilters(),
                        };
                        // if this is the first request on this session, send sticky request data which never changes
                        if (!currentPos) {
                            l.required_state = REQUIRED_STATE_EVENTS_IN_LIST;
                            l.timeline_limit = 1;
                            l.sort = [
                                "by_highlight_count",
                                "by_notification_count",
                                "by_recency",
                            ];
                        }
                        return l;
                    }),
                };
                // check if we are (un)subscribing to a room and modify request this one time for it
                let subscribingToRoom;
                if (
                    this.roomSubscription &&
                    currentSub !== this.roomSubscription
                ) {
                    if (currentSub) {
                        reqBody.unsubscribe_rooms = [currentSub];
                    }
                    reqBody.room_subscriptions = {
                        [this.roomSubscription]: {
                            required_state: REQUIRED_STATE_EVENTS_IN_ROOM,
                            timeline_limit: 30,
                        },
                    };
                    // hold a ref to the active room ID as it may change by the time we return from doSyncRequest
                    subscribingToRoom = this.roomSubscription;
                }
                resp = await this.conn.doSyncRequest(
                    accessToken,
                    currentPos,
                    reqBody
                );
                currentPos = resp.pos;
                // update what we think we're subscribed to.
                if (subscribingToRoom) {
                    currentSub = subscribingToRoom;
                }
                if (!resp.ops) {
                    resp.ops = [];
                }
                if (resp.counts) {
                    resp.counts.forEach((count, index) => {
                        this.lists[index].joinedCount = count;
                    });
                }
                this._invokeLifecycleListeners(
                    LifecycleSyncRequestFinished,
                    resp
                );
            } catch (err) {
                if (err.name !== "AbortError") {
                    this._invokeLifecycleListeners(
                        LifecycleSyncRequestFinished,
                        null,
                        err
                    );
                    await sleep(3000);
                }
            }
            if (!resp) {
                continue;
            }

            Object.keys(resp.room_subscriptions).forEach((roomId) => {
                this._invokeRoomDataListeners(
                    roomId,
                    resp.room_subscriptions[roomId]
                );
            });

            // TODO: clear gapIndex immediately after next op to avoid a genuine DELETE shifting incorrectly e.g leaving a room
            let gapIndexes = {};
            resp.counts.forEach((count, index) => {
                gapIndexes[index] = -1;
            });
            resp.ops.forEach((op) => {
                if (op.op === "DELETE") {
                    console.log("DELETE", op.list, op.index, ";");
                    delete this.lists[op.list].roomIndexToRoomId[op.index];
                    gapIndexes[op.list] = op.index;
                } else if (op.op === "INSERT") {
                    console.log(
                        "INSERT",
                        op.list,
                        op.index,
                        op.room.room_id,
                        ";"
                    );
                    if (this.lists[op.list].roomIndexToRoomId[op.index]) {
                        const gapIndex = gapIndexes[op.list];
                        // something is in this space, shift items out of the way
                        if (gapIndex < 0) {
                            console.log(
                                "cannot work out where gap is, INSERT without previous DELETE! List: ",
                                op.list
                            );
                            return;
                        }
                        //  0,1,2,3  index
                        // [A,B,C,D]
                        //   DEL 3
                        // [A,B,C,_]
                        //   INSERT E 0
                        // [E,A,B,C]
                        // gapIndex=3, op.index=0
                        if (gapIndex > op.index) {
                            // the gap is further down the list, shift every element to the right
                            // starting at the gap so we can just shift each element in turn:
                            // [A,B,C,_] gapIndex=3, op.index=0
                            // [A,B,C,C] i=3
                            // [A,B,B,C] i=2
                            // [A,A,B,C] i=1
                            // Terminate. We'll assign into op.index next.
                            for (let i = gapIndex; i > op.index; i--) {
                                if (indexInRange(op.list, i)) {
                                    this.lists[op.list].roomIndexToRoomId[i] =
                                        this.lists[op.list].roomIndexToRoomId[
                                            i - 1
                                        ];
                                }
                            }
                        } else if (gapIndex < op.index) {
                            // the gap is further up the list, shift every element to the left
                            // starting at the gap so we can just shift each element in turn
                            for (let i = gapIndex; i < op.index; i++) {
                                if (indexInRange(op.list, i)) {
                                    this.lists[op.list].roomIndexToRoomId[i] =
                                        this.lists[op.list].roomIndexToRoomId[
                                            i + 1
                                        ];
                                }
                            }
                        }
                    }
                    this.lists[op.list].roomIndexToRoomId[op.index] =
                        op.room.room_id;
                    this._invokeRoomDataListeners(op.room.room_id, op.room);
                } else if (op.op === "UPDATE") {
                    console.log(
                        "UPDATE",
                        op.list,
                        op.index,
                        op.room.room_id,
                        ";"
                    );
                    this._invokeRoomDataListeners(op.room.room_id, op.room);
                } else if (op.op === "SYNC") {
                    let syncRooms = [];
                    const startIndex = op.range[0];
                    for (let i = startIndex; i <= op.range[1]; i++) {
                        const r = op.rooms[i - startIndex];
                        if (!r) {
                            break; // we are at the end of list
                        }
                        this.lists[op.list].roomIndexToRoomId[i] = r.room_id;
                        syncRooms.push(r.room_id);
                        this._invokeRoomDataListeners(r.room_id, r);
                    }
                    console.log(
                        "SYNC",
                        op.list,
                        op.range[0],
                        op.range[1],
                        syncRooms.join(" "),
                        ";"
                    );
                } else if (op.op === "INVALIDATE") {
                    let invalidRooms = [];
                    const startIndex = op.range[0];
                    for (let i = startIndex; i <= op.range[1]; i++) {
                        invalidRooms.push(
                            this.lists[op.list].roomIndexToRoomId[i]
                        );
                        delete this.lists[op.list].roomIndexToRoomId[i];
                    }
                    console.log(
                        "INVALIDATE",
                        op.list,
                        op.range[0],
                        op.range[1],
                        ";"
                    );
                }
            });

            this._invokeLifecycleListeners(LifecycleSyncComplete, resp);
        }
    }
}

const sleep = (ms) => {
    return new Promise((resolve) => setTimeout(resolve, ms));
};

// SYNC 0 2 a b c; SYNC 6 8 d e f; DELETE 7; INSERT 0 e;
// 0 1 2 3 4 5 6 7 8
// a b c       d e f
// a b c       d _ f
// e a b c       d f  <--- c=3 is wrong as we are not tracking it, ergo we need to see if `i` is in range else drop it
const indexInRange = (listIndex, i) => {
    let isInRange = false;
    activeLists[listIndex].activeRanges.forEach((r) => {
        if (r[0] <= i && i <= r[1]) {
            isInRange = true;
        }
    });
    return isInRange;
};
