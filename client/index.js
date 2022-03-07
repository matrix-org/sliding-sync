/*
 * This file contains the entry point for the client, as well as DOM interactions.
 */

import {
    SlidingList,
    SlidingSyncConnection,
    SlidingSync,
    LifecycleSyncComplete,
    LifecycleSyncRequestFinished,
} from "./sync.js";
import * as render from "./render.js";
import * as devtools from "./devtools.js";
import * as matrix from "./matrix.js";
import { List } from "./list.js";

const roomIdAttrPrefix = (listIndex) => {
    return "room-" + listIndex + "-";
};

const roomIdAttr = (listIndex, roomIndex) => {
    return roomIdAttrPrefix(listIndex) + roomIndex;
};

let syncv2ServerUrl; // will be populated with the URL of the CS API e.g 'https://matrix-client.matrix.org'
let slidingSync;
let syncConnection = new SlidingSyncConnection();

// The lists present on the UI.
// You can add/remove these at will to experiment with sliding sync filters.
let activeLists = [
    // the data model
    new SlidingList("Direct Messages", {
        is_dm: true,
    }),
    new SlidingList("Group Chats", {
        is_dm: false,
    }),
];
let roomDomLists = activeLists.map((al, index) => {
    // the DOM model for UI purposes. It has no reference to SlidingList at all, it just knows about DOM/UI
    return new List(roomIdAttrPrefix(index), 100, (start, end) => {
        console.log("Intersection indexes for list ", index, ":", start, end);
        const bufferRange = 5;
        start = start - bufferRange < 0 ? 0 : start - bufferRange;
        end =
            end + bufferRange >= al.joinedCount
                ? al.joinedCount - 1
                : end + bufferRange;
        // we don't need to request rooms between 0,20 as we always have a filter for this
        if (end <= 20) {
            return;
        }
        // ensure we don't overlap with the 0,20 range
        if (start < 20) {
            start = 20;
        }
        // update the data model
        al.activeRanges[1] = [start, end];
        // interrupt the sync connection to send up new ranges
        syncConnection.abort();
    });
});

// this is the main data structure the client uses to remember and render rooms. Attach it to
// the window to allow easy introspection.
let rooms = {
    // this map is never deleted and forms the persistent storage of the client
    roomIdToRoom: {},
};
window.rooms = rooms;
window.activeLists = activeLists;

/**
 * Set top-level properties on the response which corresponds to state fields the UI requires.
 * The normal response returns whole state events in an array which isn't useful when rendering as we
 * often just need a single key within the content, so pre-emptively find the fields we want and set them.
 * Modifies the input object.
 * @param {object} r The room response JSON
 */
const setRoomStateFields = (r) => {
    if (!r.required_state) {
        return;
    }
    for (let i = 0; i < r.required_state.length; i++) {
        const ev = r.required_state[i];
        switch (ev.type) {
            case "m.room.avatar":
                r.avatar = ev.content.url;
                break;
            case "m.room.topic":
                r.topic = ev.content.topic;
                break;
            case "m.room.tombstone":
                r.obsolete = ev.content.body || "m.room.tombstone";
                break;
        }
    }
};

/**
 * Accumulate room data for this room. This is done by inspecting the 'initial' flag on the response.
 * If it is set, the entire room is replaced with this data. If it is false, the room data is
 * merged together with whatever is there in the store.
 * @param {object} r The room response JSON
 */
const accumulateRoomData = (r) => {
    setRoomStateFields(r);
    if (r.initial) {
        rooms.roomIdToRoom[r.room_id] = r;
        return;
    }
    // use what we already have, if any
    let existingRoom = rooms.roomIdToRoom[r.room_id];
    if (existingRoom) {
        if (r.name) {
            existingRoom.name = r.name;
        }
        if (r.highlight_count !== undefined) {
            existingRoom.highlight_count = r.highlight_count;
        }
        if (r.notification_count !== undefined) {
            existingRoom.notification_count = r.notification_count;
        }
        if (r.timeline) {
            r.timeline.forEach((e) => {
                existingRoom.timeline.push(e);
            });
        }
    } else {
        // we don't know this room but apparently we should. Whine about it and use what we have.
        console.error(
            "room initial flag not set but we have no memory of this room",
            r
        );
        existingRoom = r;
    }

    rooms.roomIdToRoom[existingRoom.room_id] = existingRoom;
};

const onRoomClick = (e) => {
    let listIndex = -1;
    let index = -1;
    // walk up the pointer event path until we find a room-##-## id=
    const path = e.composedPath();
    for (let i = 0; i < path.length; i++) {
        if (path[i].id && path[i].id.startsWith("room-")) {
            const indexes = path[i].id.substr("room-".length).split("-");
            listIndex = Number(indexes[0]);
            index = Number(indexes[1]);
            break;
        }
    }
    if (index === -1) {
        console.log("failed to find room for onclick");
        return;
    }
    // assign room subscription
    slidingSync.roomSubscription =
        activeLists[listIndex].roomIndexToRoomId[index];
    renderRoomTimeline(rooms.roomIdToRoom[slidingSync.roomSubscription], true);
    // get the highlight on the room
    renderLists();
    // interrupt the sync to get extra state events
    syncConnection.abort();
};

const renderRoomTimeline = (room, refresh) => {
    if (!room) {
        console.error(
            "renderRoomTimeline: cannot render room timeline: unknown active room ID ",
            slidingSync.roomSubscription
        );
        return;
    }
    const container = document.getElementById("messages");
    if (refresh) {
        // wipe all message entries
        while (container.hasChildNodes()) {
            container.removeChild(container.firstChild);
        }
    }
    render.renderRoomHeader(room, syncv2ServerUrl);

    // insert timeline messages
    (room.timeline || []).forEach((ev) => {
        const eventIdKey = "msg" + ev.event_id;
        const msgCell = render.renderEvent(eventIdKey, ev);
        container.appendChild(msgCell);
    });
    if (container.lastChild) {
        container.lastChild.scrollIntoView();
    }
};

const renderLists = () => {
    const roomListElements = document.getElementsByClassName("roomlist");
    for (let i = 0; i < roomListElements.length; i++) {
        let listContainer = roomListElements[i];
        let slidingList = activeLists[i];
        let domList = roomDomLists[i];
        if (!domList || !slidingList) {
            console.error(
                "renderLists(): cannot render list at index ",
                i,
                " no data associated with this index!"
            );
            continue;
        }
        domList.resize(listContainer, slidingList.joinedCount, (roomIndex) => {
            const template = document.getElementById("roomCellTemplate");
            // https://developer.mozilla.org/en-US/docs/Web/HTML/Element/template#avoiding_documentfragment_pitfall
            const roomCell = template.content.firstElementChild.cloneNode(true);
            roomCell.setAttribute("id", roomIdAttr(i, roomIndex));
            roomCell.addEventListener("click", onRoomClick);
            return roomCell;
        });

        // loop all elements and modify the contents
        for (let i = 0; i < listContainer.children.length; i++) {
            const roomCell = listContainer.children[i];
            const roomId = slidingList.roomIndexToRoomId[i];
            const r = rooms.roomIdToRoom[roomId];
            render.renderRoomCell(
                roomCell,
                r,
                i,
                r ? r.room_id === slidingSync.roomSubscription : false,
                syncv2ServerUrl
            );
        }
    }
};

const doSyncLoop = async (accessToken) => {
    if (slidingSync) {
        console.log("Terminating old loop");
        slidingSync.stop();
    }
    console.log("Starting sync loop");
    slidingSync = new SlidingSync(activeLists, syncConnection);
    slidingSync.addLifecycleListener((state, resp, err) => {
        switch (state) {
            // The sync has been processed and we can now re-render the UI.
            case LifecycleSyncComplete:
                // this list matches the list in activeLists
                renderLists();

                // check for duplicates and rooms outside tracked ranges which should never happen but can if there's a bug
                activeLists.forEach((list, listIndex) => {
                    let roomIdToPositions = {};
                    let dupeRoomIds = new Set();
                    let indexesOutsideRanges = new Set();
                    Object.keys(list.roomIndexToRoomId).forEach((i) => {
                        let rid = list.roomIndexToRoomId[i];
                        if (!rid) {
                            return;
                        }
                        let positions = roomIdToPositions[rid] || [];
                        positions.push(i);
                        roomIdToPositions[rid] = positions;
                        if (positions.length > 1) {
                            dupeRoomIds.add(rid);
                        }
                        let isInsideRange = false;
                        list.activeRanges.forEach((r) => {
                            if (i >= r[0] && i <= r[1]) {
                                isInsideRange = true;
                            }
                        });
                        if (!isInsideRange) {
                            indexesOutsideRanges.add(i);
                        }
                    });
                    dupeRoomIds.forEach((rid) => {
                        console.log(
                            rid,
                            "in list",
                            listIndex,
                            "has duplicate indexes:",
                            roomIdToPositions[rid]
                        );
                    });
                    if (indexesOutsideRanges.size > 0) {
                        console.log(
                            "list",
                            listIndex,
                            "tracking indexes outside of tracked ranges:",
                            JSON.stringify([...indexesOutsideRanges])
                        );
                    }
                });

                devtools.svgify(
                    document.getElementById("listgraph"),
                    activeLists,
                    resp
                );
                break;
            // A sync request has been finished, possibly with an error.
            case LifecycleSyncRequestFinished:
                if (err) {
                    console.error("/sync failed:", err);
                    document.getElementById("errorMsg").textContent = err;
                } else {
                    document.getElementById("errorMsg").textContent = "";
                }
                break;
        }
    });

    slidingSync.addRoomDataListener((roomId, roomData) => {
        accumulateRoomData(roomData);
        // render the right-hand side section with the room timeline if we are viewing it.
        if (roomId !== slidingSync.roomSubscription) {
            return;
        }
        let room = rooms.roomIdToRoom[slidingSync.roomSubscription];
        renderRoomTimeline(room, roomData.initial);
    });

    // begin the sliding sync loop
    slidingSync.start(accessToken);
};

// Main entry point to the client is here
window.addEventListener("load", async (event) => {
    // Download the base CS API server URL from the sliding sync proxy.
    // We need to know the base URL for media requests, sending events, etc.
    const v2ServerResp = await fetch("./server.json");
    const syncv2ServerJson = await v2ServerResp.json();
    if (!syncv2ServerJson || !syncv2ServerJson.server) {
        console.error("failed to fetch v2 server url: ", v2ServerResp);
        return;
    }
    syncv2ServerUrl = syncv2ServerJson.server.replace(/\/$/, ""); // remove trailing /

    // Dynamically create the room lists based on the `activeLists` variable.
    // This exists to allow developers to experiment with different lists and filters.
    const container = document.getElementById("roomlistcontainer");
    activeLists.forEach((list) => {
        const roomList = document.createElement("div");
        roomList.className = "roomlist";
        const roomListName = document.createElement("div");
        roomListName.className = "roomlistname";
        roomListName.textContent = list.name;
        const roomListWrapper = document.createElement("div");
        roomListWrapper.className = "roomlistwrapper";
        roomListWrapper.appendChild(roomListName);
        roomListWrapper.appendChild(roomList);
        container.appendChild(roomListWrapper);
    });

    // Load any stored access token.
    const storedAccessToken = window.localStorage.getItem("accessToken");
    if (storedAccessToken) {
        document.getElementById("accessToken").value = storedAccessToken;
    }

    // hook up the sync button to start the sync loop
    document.getElementById("syncButton").onclick = () => {
        const accessToken = document.getElementById("accessToken").value;
        window.localStorage.setItem("accessToken", accessToken); // remember token over refreshes
        doSyncLoop(accessToken);
    };

    // hook up the room filter so it filters as the user types
    document.getElementById("roomfilter").addEventListener("input", (ev) => {
        const roomNameFilter = ev.target.value;
        for (let i = 0; i < activeLists.length; i++) {
            // apply the room name filter to all lists
            const filters = activeLists[i].getFilters();
            filters.room_name_like = roomNameFilter;
            activeLists[i].setFilters(filters);
        }
        // bump to the start of the room list again. We need to do this to ensure the UI displays correctly.
        const lists = document.getElementsByClassName("roomlist");
        for (let i = 0; i < lists.length; i++) {
            if (lists[i].firstChild) {
                lists[i].firstChild.scrollIntoView(true);
            }
        }
        // interrupt the sync request to send up new filters
        syncConnection.abort();
    });

    // hook up the send message input
    document
        .getElementById("sendmessageinput")
        .addEventListener("keydown", async (ev) => {
            if (ev.key == "Enter") {
                ev.target.setAttribute("disabled", "");
                const msg = ev.target.value;
                try {
                    await matrix.sendMessage(
                        syncv2ServerUrl,
                        document.getElementById("accessToken").value,
                        slidingSync.roomSubscription,
                        msg
                    );
                    ev.target.value = "";
                } catch (err) {
                    document.getElementById("errorMsg").textContent =
                        "Error sending message: " + err;
                }
                ev.target.removeAttribute("disabled");
                ev.target.focus();
            }
        });
});
