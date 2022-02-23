// This file contains the entry point for the client, as well as DOM interactions.
import { SlidingList  } from './sync.js';
import * as render from './render.js';

let lastError = null;
let activeAbortController = new AbortController();
let activeSessionId;
let activeRoomId = ""; // the room currently being viewed
let txBytes = 0;
let rxBytes = 0;

let activeLists = [
    new SlidingList("Direct Messages", {
        is_dm: true,
    }),
    new SlidingList("Group Chats", {
        is_dm: false,
    }),
];

const requiredStateEventsInList = [
    ["m.room.avatar", ""],
    ["m.room.tombstone", ""],
];
const requiredStateEventsInRoom = [
    ["m.room.avatar", ""],
    ["m.room.topic", ""],
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
            console.log("Intersection indexes:", JSON.stringify(listIndexToStartEnd));
            // buffer range
            const bufferRange = 5;

            Object.keys(listIndexToStartEnd).forEach((listIndex) => {
                let startIndex = listIndexToStartEnd[listIndex].startIndex;
                let endIndex = listIndexToStartEnd[listIndex].endIndex;
                startIndex = startIndex - bufferRange < 0 ? 0 : startIndex - bufferRange;
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
            activeAbortController.abort();
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
    // assign global state
    activeRoomId = activeLists[listIndex].roomIndexToRoomId[index];
    renderRoomContent(activeRoomId, true);
    // get the highlight on the room
    const roomListElements = document.getElementsByClassName("roomlist");
    for (let i = 0; i < roomListElements.length; i++) {
        renderList(roomListElements[i], i);
    }
    // interrupt the sync to get extra state events
    activeAbortController.abort();
};

const renderRoomContent = (roomId, refresh) => {
    if (roomId !== activeRoomId) {
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
    let room = rooms.roomIdToRoom[activeRoomId];
    if (!room) {
        console.error("renderRoomContent: unknown active room ID ", activeRoomId);
        return;
    }
    document.getElementById("selectedroomname").textContent = room.name || room.room_id;
    if (room.avatar) {
        document.getElementById("selectedroomavatar").src =
            mxcToUrl(room.avatar) || "/client/placeholder.svg";
    } else {
        document.getElementById("selectedroomavatar").src = "/client/placeholder.svg";
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

const roomIdAttr = (listIndex, roomIndex) => {
    return "room-" + listIndex + "-" + roomIndex;
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
    let addCount = 0;
    let removeCount = 0;
    // ensure we have the right number of children, remove or add appropriately.
    while (container.childElementCount > listData.joinedCount) {
        intersectionObserver.unobserve(container.lastChild);
        container.removeChild(container.lastChild);
        removeCount += 1;
    }
    for (let i = container.childElementCount; i < listData.joinedCount; i++) {
        const template = document.getElementById("roomCellTemplate");
        // https://developer.mozilla.org/en-US/docs/Web/HTML/Element/template#avoiding_documentfragment_pitfall
        const roomCell = template.content.firstElementChild.cloneNode(true);
        roomCell.setAttribute("id", roomIdAttr(listIndex, i));
        container.appendChild(roomCell);
        intersectionObserver.observe(roomCell);
        roomCell.addEventListener("click", onRoomClick);
        addCount += 1;
    }
    if (addCount > 0 || removeCount > 0) {
        console.log("render: added ", addCount, "nodes, removed", removeCount, "nodes");
    }
    // loop all elements and modify the contents
    for (let i = 0; i < container.children.length; i++) {
        const roomCell = container.children[i];
        const roomId = listData.roomIndexToRoomId[i];
        const r = rooms.roomIdToRoom[roomId];
        const roomNameSpan = roomCell.getElementsByClassName("roomname")[0];
        const roomContentSpan = roomCell.getElementsByClassName("roomcontent")[0];
        const roomSenderSpan = roomCell.getElementsByClassName("roomsender")[0];
        const roomTimestampSpan = roomCell.getElementsByClassName("roomtimestamp")[0];
        const unreadCountSpan = roomCell.getElementsByClassName("unreadcount")[0];
        unreadCountSpan.textContent = "";
        unreadCountSpan.classList.remove("unreadcountnotify");
        unreadCountSpan.classList.remove("unreadcounthighlight");
        if (!r) {
            // placeholder
            roomNameSpan.textContent = randomName(i, false);
            roomNameSpan.style = "background: #e0e0e0; color: #e0e0e0;";
            roomContentSpan.textContent = randomName(i, true);
            roomContentSpan.style = "background: #e0e0e0; color: #e0e0e0;";
            roomSenderSpan.textContent = "";
            roomTimestampSpan.textContent = "";
            roomCell.getElementsByClassName("roomavatar")[0].src = "/client/placeholder.svg";
            roomCell.style = "";
            continue;
        }
        roomCell.style = "";
        roomNameSpan.textContent = r.name || r.room_id;
        roomNameSpan.style = "";
        roomContentSpan.style = "";
        if (r.avatar) {
            roomCell.getElementsByClassName("roomavatar")[0].src =
                mxcToUrl(r.avatar) || "/client/placeholder.svg";
        } else {
            roomCell.getElementsByClassName("roomavatar")[0].src = "/client/placeholder.svg";
        }
        if (roomId === activeRoomId) {
            roomCell.style = "background: #d7d7f7";
        }
        if (r.highlight_count > 0) {
            // use the notification count instead to avoid counts dropping down. This matches ele-web
            unreadCountSpan.textContent = r.notification_count + "";
            unreadCountSpan.classList.add("unreadcounthighlight");
        } else if (r.notification_count > 0) {
            unreadCountSpan.textContent = r.notification_count + "";
            unreadCountSpan.classList.add("unreadcountnotify");
        } else {
            unreadCountSpan.textContent = "";
        }

        if (r.obsolete) {
            roomContentSpan.textContent = "";
            roomSenderSpan.textContent = r.obsolete;
        } else if (r.timeline && r.timeline.length > 0) {
            const mostRecentEvent = r.timeline[r.timeline.length - 1];
            roomSenderSpan.textContent = mostRecentEvent.sender;
            // TODO: move to render.js
            roomTimestampSpan.textContent = render.formatTimestamp(mostRecentEvent.origin_server_ts);

            const body = render.textForEvent(mostRecentEvent);
            if (mostRecentEvent.type === "m.room.member") {
                roomContentSpan.textContent = "";
                roomSenderSpan.textContent = body;
            } else {
                roomContentSpan.textContent = body;
            }
        } else {
            roomContentSpan.textContent = "";
        }
    }
};
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

const doSyncLoop = async (accessToken, sessionId) => {
    console.log("Starting sync loop. Active: ", activeSessionId, " this:", sessionId);

    let currentPos;
    let currentError = null;
    let currentSub = "";
    while (sessionId === activeSessionId) {
        let resp;
        try {
            // these fields are always required
            let reqBody = {
                lists: activeLists.map((al) => {
                    let l = {
                        ranges: al.activeRanges,
                        filters: al.getFilters(),
                    };
                    // if this is the first request on this session, send sticky request data which never changes
                    if (!currentPos) {
                        l.required_state = requiredStateEventsInList;
                        l.timeline_limit = 1;
                        l.sort = ["by_highlight_count", "by_notification_count", "by_recency"];
                    }
                    return l;
                }),
            };
            // check if we are (un)subscribing to a room and modify request this one time for it
            let subscribingToRoom;
            if (activeRoomId && currentSub !== activeRoomId) {
                if (currentSub) {
                    reqBody.unsubscribe_rooms = [currentSub];
                }
                reqBody.room_subscriptions = {
                    [activeRoomId]: {
                        required_state: requiredStateEventsInRoom,
                        timeline_limit: 30,
                    },
                };
                // hold a ref to the active room ID as it may change by the time we return from doSyncRequest
                subscribingToRoom = activeRoomId;
            }
            resp = await doSyncRequest(accessToken, currentPos, reqBody);
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
                    activeLists[index].joinedCount = count;
                });
            }
        } catch (err) {
            if (err.name !== "AbortError") {
                console.error("/sync failed:", err);
                console.log("current", currentError, "last", lastError);
                if (currentError != lastError) {
                    document.getElementById("errorMsg").textContent = lastError ? lastError : "";
                }
                currentError = lastError;
                await sleep(3000);
            }
        }
        if (!resp) {
            continue;
        }

        Object.keys(resp.room_subscriptions).forEach((roomId) => {
            accumulateRoomData(
                resp.room_subscriptions[roomId],
                rooms.roomIdToRoom[roomId] !== undefined
            );
            renderRoomContent(roomId);
        });

        // TODO: clear gapIndex immediately after next op to avoid a genuine DELETE shifting incorrectly e.g leaving a room
        let gapIndexes = {};
        resp.counts.forEach((count, index) => {
            gapIndexes[index] = -1;
        });
        resp.ops.forEach((op) => {
            if (op.op === "DELETE") {
                console.log("DELETE", op.list, op.index, ";");
                delete activeLists[op.list].roomIndexToRoomId[op.index];
                gapIndexes[op.list] = op.index;
            } else if (op.op === "INSERT") {
                console.log("INSERT", op.list, op.index, op.room.room_id, ";");
                if (activeLists[op.list].roomIndexToRoomId[op.index]) {
                    const gapIndex = gapIndexes[op.list];
                    // something is in this space, shift items out of the way
                    if (gapIndex < 0) {
                        console.log(
                            "cannot work out where gap is, INSERT without previous DELETE! List: ", op.list,
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
                                activeLists[op.list].roomIndexToRoomId[i] =
                                    activeLists[op.list].roomIndexToRoomId[i - 1];
                            }
                        }
                    } else if (gapIndex < op.index) {
                        // the gap is further up the list, shift every element to the left
                        // starting at the gap so we can just shift each element in turn
                        for (let i = gapIndex; i < op.index; i++) {
                            if (indexInRange(op.list, i)) {
                                activeLists[op.list].roomIndexToRoomId[i] =
                                    activeLists[op.list].roomIndexToRoomId[i + 1];
                            }
                        }
                    }
                }
                accumulateRoomData(op.room, rooms.roomIdToRoom[op.room.room_id] !== undefined);
                activeLists[op.list].roomIndexToRoomId[op.index] = op.room.room_id;
                renderRoomContent(op.room.room_id);
            } else if (op.op === "UPDATE") {
                console.log("UPDATE", op.list, op.index, op.room.room_id, ";");
                accumulateRoomData(op.room, true);
                renderRoomContent(op.room.room_id);
            } else if (op.op === "SYNC") {
                let syncRooms = [];
                const startIndex = op.range[0];
                for (let i = startIndex; i <= op.range[1]; i++) {
                    const r = op.rooms[i - startIndex];
                    if (!r) {
                        break; // we are at the end of list
                    }
                    activeLists[op.list].roomIndexToRoomId[i] = r.room_id;
                    syncRooms.push(r.room_id);
                    accumulateRoomData(r);
                }
                console.log("SYNC", op.list, op.range[0], op.range[1], syncRooms.join(" "), ";");
            } else if (op.op === "INVALIDATE") {
                let invalidRooms = [];
                const startIndex = op.range[0];
                for (let i = startIndex; i <= op.range[1]; i++) {
                    invalidRooms.push(activeLists[op.list].roomIndexToRoomId[i]);
                    delete activeLists[op.list].roomIndexToRoomId[i];
                }
                console.log("INVALIDATE", op.list, op.range[0], op.range[1], ";");
            }
        });
        const roomListElements = document.getElementsByClassName("roomlist");
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

        svgify(resp);
    }
    console.log("active session: ", activeSessionId, " this session: ", sessionId, " terminating.");
};
// accessToken = string, pos = int, ranges = [2]int e.g [0,99]
let doSyncRequest = async (accessToken, pos, reqBody) => {
    activeAbortController = new AbortController();
    const jsonBody = JSON.stringify(reqBody);
    let resp = await fetch("/_matrix/client/v3/sync" + (pos ? "?pos=" + pos : ""), {
        signal: activeAbortController.signal,
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
    txBytes += jsonBody.length;
    rxBytes += JSON.stringify(respBody).length;
    document.getElementById("txrx").textContent = (txBytes/1024.0).toFixed(2) + " KB Tx / " + (rxBytes/1024.0).toFixed(2) + " KB Rx";

    if (resp.status != 200) {
        if (respBody.error) {
            lastError = respBody.error;
        }
        throw new Error("/sync returned HTTP " + resp.status + " " + respBody.error);
    }
    lastError = null;
    return respBody;
};

const randomName = (i, long) => {
    if (i % 17 === 0) {
        return long
            ? "Ever have that feeling where you’re not sure if you’re awake or dreaming?"
            : "There is no spoon";
    } else if (i % 13 === 0) {
        return long
            ? "Choice is an illusion created between those with power and those without."
            : "Get Up Trinity";
    } else if (i % 11 === 0) {
        return long
            ? "That’s how it is with people. Nobody cares how it works as long as it works."
            : "I know kung fu";
    } else if (i % 7 === 0) {
        return long ? "The body cannot live without the mind." : "Free your mind";
    } else if (i % 5 === 0) {
        return long ? "Perhaps we are asking the wrong questions…" : "Agent Smith";
    } else if (i % 3 === 0) {
        return long ? "You've been living in a dream world, Neo." : "Mr Anderson";
    } else {
        return long ? "Mr. Wizard, get me the hell out of here! " : "Morpheus";
    }
};

const svgify = (resp) => {
    if (resp.ops.length === 0) {
        return;
    }
    const horizontalPixelWidth = 10;
    let verticalPixelHeight = 1;
    const listgraph = document.getElementById("listgraph");
    while (listgraph.firstChild) {
        listgraph.removeChild(listgraph.firstChild);
    }
    const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
    svg.setAttribute("style", "background-color:black;");
    // column 0 = list[0], column 2 = list[1], etc...
    // 1 pixel per room so svg is however many rooms the user has
    svg.setAttribute("width", 2 * activeLists.length * horizontalPixelWidth);
    let height = 1;
    activeLists.forEach((al) => {
        if (al.joinedCount > height) {
            height = al.joinedCount;
        }
    })
    if (height < (window.innerHeight/2)) { // we can double the vertical pixel height to make it easier to see
        verticalPixelHeight = 2;
    }
    svg.setAttribute("height", height * verticalPixelHeight);
    const colorInWindow = "#2020f0";
    const colorPlaceholder = "#404040";
    const colorDelete = "#ff0000";
    const colorInsert = "#00ff00";
    const colorUpdate = "#00ffff";
    const colorSync = "#ffff00";
    const colorInvalidate = "#500000";
    const colorRoom = "#ffffff";
    activeLists.forEach((al, index) => {
        const placeholders = document.createElementNS("http://www.w3.org/2000/svg",'rect');
        placeholders.setAttribute("x", index*2*horizontalPixelWidth);
        placeholders.setAttribute("y", 0);
        placeholders.setAttribute("width", horizontalPixelWidth);
        placeholders.setAttribute("height", al.joinedCount * verticalPixelHeight);
        placeholders.setAttribute('fill', colorPlaceholder);

        svg.appendChild(placeholders);
        // [[0, 20], [50,60]];
        al.activeRanges.forEach((range) => {
            const rect = document.createElementNS("http://www.w3.org/2000/svg",'rect');
            rect.setAttribute('x',index*2*horizontalPixelWidth);
            rect.setAttribute('y',range[0]*verticalPixelHeight);
            rect.setAttribute('width',horizontalPixelWidth);
            rect.setAttribute('height',(range[1]-range[0]) * verticalPixelHeight);
            rect.setAttribute('fill',colorInWindow);
            svg.appendChild(rect);
        });
    });

    const addLine = (index, y, colour, yLen) => {
        const bar = document.createElementNS("http://www.w3.org/2000/svg",'rect');
        bar.setAttribute("x", index*2*horizontalPixelWidth);
        bar.setAttribute("y", y*verticalPixelHeight);
        bar.setAttribute('width',horizontalPixelWidth);
        bar.setAttribute('height',verticalPixelHeight*(yLen?yLen:1));
        bar.setAttribute('fill', colour);
        const animation = document.createElementNS("http://www.w3.org/2000/svg","animate");
        animation.setAttribute("attributeName", "visibility");
        animation.setAttribute("from", "visible");
        animation.setAttribute("to", "hidden");
        animation.setAttribute("dur", "0.5s");
        animation.setAttribute("repeatCount", "3");
        bar.appendChild(animation);
        svg.appendChild(bar);
    }

    // add insertions, deletions and updates
    resp.ops.forEach((op) => {
        if (op.op === "DELETE") {
            addLine(op.list, op.index, colorDelete);
        } else if (op.op === "INSERT") {
            addLine(op.list, op.index, colorInsert);
        } else if (op.op === "UPDATE") {
            addLine(op.list, op.index, colorUpdate);
        } else if (op.op === "SYNC") {
            addLine(op.list, op.range[0], colorSync, op.range[1]-op.range[0]+1);
        } else if (op.op === "INVALIDATE") {
            addLine(op.list, op.range[0], colorInvalidate, op.range[1]-op.range[0]+1);
        }
    });

    // this is expensive so only do it on smaller accounts
    if (height < 500 && false) {
        const fifth = horizontalPixelWidth/5;
        // draw white dot for each room which has some kind of data stored
        activeLists.forEach((al, index) => {
            for (let roomIndex of Object.keys(al.roomIndexToRoomId)) {
                const roomPixel = document.createElementNS("http://www.w3.org/2000/svg",'rect');
                roomPixel.setAttribute("x", index*2*horizontalPixelWidth + fifth);
                roomPixel.setAttribute("y", roomIndex*verticalPixelHeight);
                roomPixel.setAttribute('width',fifth);
                roomPixel.setAttribute('height',verticalPixelHeight);
                roomPixel.setAttribute('fill', colorRoom);
                svg.appendChild(roomPixel);
            }
        });
    }

    /*
    const animation = document.createElementNS("http://www.w3.org/2000/svg","animate");
    animation.setAttribute("attributeName", "y");
    animation.setAttribute("from", al.joinedCount);
    animation.setAttribute("to", 0);
    animation.setAttribute("dur", "1s");
    animation.setAttribute("fill", "freeze");
    deleteBar.appendChild(animation); */
    



    listgraph.appendChild(svg);
}

const mxcToUrl = (mxc) => {
    const path = mxc.substr("mxc://".length);
    if (!path) {
        return;
    }
    // TODO: we should really use the proxy HS not matrix.org
    return `https://matrix-client.matrix.org/_matrix/media/r0/thumbnail/${path}?width=64&height=64&method=crop`;
};

window.addEventListener("load", (event) => {
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
    const storedAccessToken = window.localStorage.getItem("accessToken");
    if (storedAccessToken) {
        document.getElementById("accessToken").value = storedAccessToken;
    }
    document.getElementById("syncButton").onclick = () => {
        const accessToken = document.getElementById("accessToken").value;
        if (accessToken === "debug") {
            document.getElementById("debugButton").style = "";
            return;
        }
        window.localStorage.setItem("accessToken", accessToken);
        activeSessionId = new Date().getTime() + "";
        doSyncLoop(accessToken, activeSessionId);
    };
    document.getElementById("roomfilter").addEventListener("input", (ev) => {
        const roomNameFilter = ev.target.value;
        for (let i = 0; i < activeLists.length; i++) {
            const filters = activeLists[i].getFilters();
            filters.room_name_like = roomNameFilter;
            activeLists[i].setFilters(filters);
        }
        // bump to the start of the room list again
        const lists = document.getElementsByClassName("roomlist");
        for (let i = 0; i < lists.length; i++) {
            if (lists[i].firstChild) {
                lists[i].firstChild.scrollIntoView(true);
            }
        }
        if (activeAbortController) {
            // interrupt the sync request to send up new filters
            activeAbortController.abort();
        }
    });
    document.getElementById("debugButton").onclick = () => {
        const debugBox = document.getElementById("debugCmd");
        debugBox.style = "";
        alert(
            "Type sync operations and press ENTER to execute locally. Examples:\n" +
                "SYNC 0 5 a b c d e f\n" +
                "DELETE 0; INSERT 1 f\n" +
                "UPDATE 0 a\n"
        );

        awaitingPromises = {};
        window.responseQueue = [];
        // monkey patch the sync request command to read from us and not do network requests
        doSyncRequest = async (accessToken, pos, reqBody) => {
            if (!pos) {
                pos = 0;
            }
            let r = window.responseQueue[pos];
            if (r) {
                r.pos = pos + 1; // client should request next pos next
                console.log(r);
                return r;
            }
            // else wait until we have a response at this position
            const responsePromise = new Promise((resolve) => {
                awaitingPromises[pos] = resolve;
            });
            console.log("DEBUG: waiting for position ", pos);
            r = await responsePromise;
            console.log(r);
            return r;
        };
        const fakeRoom = (s) => {
            return {
                highlight_count: 0,
                notification_count: 0,
                room_id: s,
                name: s,
                timeline: [
                    {
                        type: "m.room.message",
                        sender: "DEBUG",
                        content: {
                            body: "Debug message",
                        },
                        origin_server_ts: new Date().getTime(),
                    },
                ],
            };
        };
        let started = false;
        debugBox.onkeypress = (ev) => {
            if (ev.key !== "Enter") {
                return;
            }
            if (!started) {
                started = true;
                activeSessionId = "debug";
                doSyncLoop("none", "debug");
            }
            const debugInput = debugBox.value;
            debugBox.value = "";
            const cmds = debugInput.split(";");
            let ops = [];
            cmds.forEach((cmd) => {
                cmd = cmd.trim();
                if (cmd.length == 0) {
                    return;
                }
                const args = cmd.split(" ");
                console.log(args);
                switch (args[0].toUpperCase()) {
                    case "SYNC": // SYNC 0 5 a b c d e f
                        const rooms = args.slice(3);
                        if (rooms.length != 1 + Number(args[2]) - Number(args[1])) {
                            console.error(
                                "Bad SYNC: got ",
                                rooms.length,
                                " rooms for range (indexes are inclusive), ignoring."
                            );
                            return;
                        }
                        ops.push({
                            range: [Number(args[1]), Number(args[2])],
                            op: "SYNC",
                            rooms: rooms.map((s) => {
                                return fakeRoom(s);
                            }),
                        });
                        break;
                    case "INVALIDATE": // INVALIDATE 0 5
                        ops.push({
                            range: [Number(args[1]), Number(args[2])],
                            op: "INVALIDATE",
                        });
                        break;
                    case "INSERT": // INSERT 5 a
                        ops.push({
                            index: Number(args[1]),
                            room: fakeRoom(args[2]),
                            op: "INSERT",
                        });
                        break;
                    case "UPDATE": // UPDATE 3 b
                        ops.push({
                            index: Number(args[1]),
                            room: fakeRoom(args[2]),
                            op: "UPDATE",
                        });
                        break;
                    case "DELETE": // DELETE 2
                        ops.push({
                            index: Number(args[1]),
                            op: "DELETE",
                        });
                        break;
                }
            });

            responseQueue.push({
                count: 256,
                ops: ops,
                room_subscriptions: {},
            });
            // check if there are waiting requests and wake them up
            const thisPos = responseQueue.length - 1;
            const resolve = awaitingPromises[thisPos];
            if (resolve) {
                console.log("DEBUG: waking up for position ", thisPos);
                resolve(responseQueue[thisPos]);
            }
        };
    };
});
