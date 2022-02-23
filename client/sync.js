// This file contains the main sliding sync code.

// The default range to /always/ track on a list.
// When you scroll the list, new windows are added to the first element. E.g [[0,20], [37,45]]
// TODO: explain why
const DEFAULT_RANGES = [[0, 20]];

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