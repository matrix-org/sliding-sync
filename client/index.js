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

let syncv2ServerUrl; // will be populated with the URL of the CS API e.g 'https://matrix-client.matrix.org'
let slidingSync;
let syncConnection = new SlidingSyncConnection();
let activeLists = [
    new SlidingList("Direct Messages", {
        is_dm: true,
    }),
    new SlidingList("Group Chats", {
        is_dm: false,
    }),
];

// this is the main data structure the client uses to remember and render rooms. Attach it to
// the window to allow easy introspection.
let rooms = {
    // this map is never deleted and forms the persistent storage of the client
    roomIdToRoom: {},
};
window.rooms = rooms;
window.activeLists = activeLists;

const accumulateRoomData = (r, isUpdate) => {
    let room = r;
    if (isUpdate) {
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
            room = existingRoom;
        }
    }
    // pull out avatar and topic if it exists
    let avatar;
    let topic;
    let obsolete;
    if (r.required_state) {
        for (let i = 0; i < r.required_state.length; i++) {
            const ev = r.required_state[i];
            switch (ev.type) {
                case "m.room.avatar":
                    avatar = ev.content.url;
                    break;
                case "m.room.topic":
                    topic = ev.content.topic;
                    break;
                case "m.room.tombstone":
                    obsolete = ev.content.body || "m.room.tombstone";
                    break;
            }
        }
    }
    if (avatar !== undefined) {
        room.avatar = avatar;
    }
    if (topic !== undefined) {
        room.topic = topic;
    }
    if (obsolete !== undefined) {
        room.obsolete = obsolete;
    }
    rooms.roomIdToRoom[room.room_id] = room;
};

let debounceTimeoutId;
let visibleIndexes = {}; // e.g "1-44" meaning list 1 index 44

const intersectionObserver = new IntersectionObserver(
    (entries) => {
        entries.forEach((entry) => {
            let key = entry.target.id.substr("room-".length);
            if (entry.isIntersecting) {
                visibleIndexes[key] = true;
            } else {
                delete visibleIndexes[key];
            }
        });
        // we will process the intersections after a short period of inactivity to not thrash the server
        clearTimeout(debounceTimeoutId);
        debounceTimeoutId = setTimeout(() => {
            let listIndexToStartEnd = {};
            Object.keys(visibleIndexes).forEach((indexes) => {
                // e.g "1-44"
                let [listIndex, roomIndex] = indexes.split("-");
                let i = Number(roomIndex);
                listIndex = Number(listIndex);
                if (!listIndexToStartEnd[listIndex]) {
                    listIndexToStartEnd[listIndex] = {
                        startIndex: -1,
                        endIndex: -1,
                    };
                }
                let startIndex = listIndexToStartEnd[listIndex].startIndex;
                let endIndex = listIndexToStartEnd[listIndex].endIndex;
                if (startIndex === -1 || i < startIndex) {
                    listIndexToStartEnd[listIndex].startIndex = i;
                }
                if (endIndex === -1 || i > endIndex) {
                    listIndexToStartEnd[listIndex].endIndex = i;
                }
            });
            console.log(
                "Intersection indexes:",
                JSON.stringify(listIndexToStartEnd)
            );
            // buffer range
            const bufferRange = 5;

            Object.keys(listIndexToStartEnd).forEach((listIndex) => {
                let startIndex = listIndexToStartEnd[listIndex].startIndex;
                let endIndex = listIndexToStartEnd[listIndex].endIndex;
                startIndex =
                    startIndex - bufferRange < 0 ? 0 : startIndex - bufferRange;
                endIndex =
                    endIndex + bufferRange >= activeLists[listIndex].joinedCount
                        ? activeLists[listIndex].joinedCount - 1
                        : endIndex + bufferRange;

                // we don't need to request rooms between 0,20 as we always have a filter for this
                if (endIndex <= 20) {
                    return;
                }
                // ensure we don't overlap with the 0,20 range
                if (startIndex < 20) {
                    startIndex = 20;
                }

                activeLists[listIndex].activeRanges[1] = [startIndex, endIndex];
            });
            // interrupt the sync connection to send up new ranges
            syncConnection.abort();
        }, 100);
    },
    {
        threshold: [0],
    }
);

const renderMessage = (container, ev) => {
    const eventIdKey = "msg" + ev.event_id;
    // try to find the element. If it exists then don't re-render.
    const existing = document.getElementById(eventIdKey);
    if (existing) {
        return;
    }
    const msgCell = render.renderEvent(eventIdKey, ev);
    container.appendChild(msgCell);
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
    const roomListElements = document.getElementsByClassName("roomlist");
    for (let i = 0; i < roomListElements.length; i++) {
        renderList(roomListElements[i], i);
    }
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
        document.getElementById("selectedroomname").textContent = "";
        // wipe all message entries
        while (container.hasChildNodes()) {
            container.removeChild(container.firstChild);
        }
    }
    document.getElementById("selectedroomname").textContent =
        room.name || room.room_id;
    if (room.avatar) {
        // TODO move to render.js
        document.getElementById("selectedroomavatar").src =
            render.mxcToUrl(syncv2ServerUrl, room.avatar) ||
            "/client/placeholder.svg";
    } else {
        document.getElementById("selectedroomavatar").src =
            "/client/placeholder.svg";
    }
    if (room.topic) {
        document.getElementById("selectedroomtopic").textContent = room.topic;
    } else {
        document.getElementById("selectedroomtopic").textContent = "";
    }

    // insert timeline messages
    (room.timeline || []).forEach((ev) => {
        renderMessage(container, ev);
    });
    if (container.lastChild) {
        container.lastChild.scrollIntoView();
    }
};

const renderList = (container, listIndex) => {
    const listData = activeLists[listIndex];
    if (!listData) {
        console.error(
            "renderList(): cannot render list at index ",
            listIndex,
            " no data associated with this index!"
        );
        return;
    }
    render.renderRoomList(
        container,
        syncv2ServerUrl,
        listIndex,
        listData,
        slidingSync.roomSubscription,
        rooms.roomIdToRoom,
        intersectionObserver,
        onRoomClick
    );
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
                const roomListElements =
                    document.getElementsByClassName("roomlist");
                for (let i = 0; i < roomListElements.length; i++) {
                    renderList(roomListElements[i], i);
                }

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

    slidingSync.addRoomDataListener((roomId, roomData, isIncremental) => {
        accumulateRoomData(
            roomData,
            isIncremental
                ? isIncremental
                : rooms.roomIdToRoom[roomId] !== undefined
        );
        // render the right-hand side section with the room timeline if we are viewing it.
        if (roomId !== slidingSync.roomSubscription) {
            return;
        }
        let room = rooms.roomIdToRoom[slidingSync.roomSubscription];
        renderRoomTimeline(room, false);
    });
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
});
