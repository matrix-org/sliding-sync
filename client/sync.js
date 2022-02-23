// This file contains the main sliding sync code.

import * as devtools from './devtools.js';

// The default range to /always/ track on a list.
// When you scroll the list, new windows are added to the first element. E.g [[0,20], [37,45]]
// TODO: explain why
const DEFAULT_RANGES = [[0, 20]];


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
        this.lastError = null;
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
        let resp = await fetch("/_matrix/client/v3/sync" + (pos ? "?pos=" + pos : ""), {
            signal: this.abortController.signal,
            method: "POST",
            headers: {
                Authorization: "Bearer " + accessToken,
                "Content-Type": "application/json",
            },
            body: jsonBody,
        });

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
            if (respBody.error) {
                this.lastError = respBody.error;
            }
            throw new Error("/sync returned HTTP " + resp.status + " " + respBody.error);
        }
        this.lastError = null;
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

class SlidingSync {

    /**
     * 
     * @param {[]SlidingList} activeLists 
     */
    constructor(activeLists) {

    }
}