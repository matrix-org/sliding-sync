let lastError = null;
let activeAbortController = new AbortController();
let activeSessionId;
let activeRanges = [[0,20]];
let activeRoomId = ""; // the room currently being viewed
const requiredStateEventsInList = [
    ["m.room.avatar", ""], ["m.room.tombstone", ""]
];
const requiredStateEventsInRoom = [
    ["m.room.avatar", ""], ["m.room.topic", ""],
];

// this is the main data structure the client uses to remember and render rooms. Attach it to
// the window to allow easy introspection.
let rooms = {
    // the total number of joined rooms according to the server, always >= len(roomIndexToRoomId)
    joinedCount: 0,
    // this map is never deleted and forms the persistent storage of the client
    roomIdToRoom: {},
    // the constantly changing sliding window ranges. Not an array for performance reasons
    // E.g tracking ranges 0-99, 500-599, we don't want to have a 600 element array
    roomIndexToRoomId: {},
};
window.rooms = rooms;
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
let visibleIndexes = {};

const intersectionObserver = new IntersectionObserver((entries) => {
    entries.forEach((entry) => {
        let key = entry.target.id.substr("room".length);
        if (entry.isIntersecting) {
            visibleIndexes[key] = true;
        } else {
            delete visibleIndexes[key];
        }
    });
    // we will process the intersections after a short period of inactivity to not thrash the server
    clearTimeout(debounceTimeoutId);
    debounceTimeoutId = setTimeout(() => {
        let startIndex = 0;
        let endIndex = 0;
        Object.keys(visibleIndexes).forEach((i) => {
            i = Number(i);
            startIndex = startIndex || i;
            endIndex = endIndex || i;
            if (i < startIndex) {
                startIndex = i;
            }
            if (i > endIndex) {
                endIndex = i;
            }
        });
        console.log("start", startIndex, "end", endIndex);
        // buffer range
        const bufferRange = 5;
        startIndex = (startIndex - bufferRange) < 0 ? 0 : (startIndex - bufferRange);
        endIndex = (endIndex + bufferRange) >= rooms.joinedCount ? rooms.joinedCount-1 : (endIndex + bufferRange);

        // we don't need to request rooms between 0,20 as we always have a filter for this
        if (endIndex <= 20) {
            return;
        }
        // ensure we don't overlap with the 0,20 range
        if (startIndex < 20) {
            startIndex = 20;
        }

        activeRanges[1] = [startIndex, endIndex];
        activeAbortController.abort();
        console.log("next: ", startIndex, "-", endIndex);
    }, 100);
}, {
    threshold: [0],
});

const renderMessage = (container, ev) => {
    const eventIdKey = "msg" + ev.event_id;
    // try to find the element. If it exists then don't re-render.
    const existing = document.getElementById(eventIdKey);
    if (existing) {
        return;
    }

    const template = document.getElementById("messagetemplate");
    // https://developer.mozilla.org/en-US/docs/Web/HTML/Element/template#avoiding_documentfragment_pitfall
    const msgCell = template.content.firstElementChild.cloneNode(true);
    msgCell.setAttribute("id", eventIdKey);
    msgCell.getElementsByClassName("msgsender")[0].textContent = ev.sender;
    msgCell.getElementsByClassName("msgtimestamp")[0].textContent = formatTimestamp(ev.origin_server_ts);
    let body = "";
    switch (ev.type) {
        case "m.room.message":
            body = ev.content.body;
            break;
        case "m.room.member":
            body = membershipChangeText(ev);
            break;
        default:
            body = ev.type + " event";
            break;
    }
    msgCell.getElementsByClassName("msgcontent")[0].textContent = body;
    container.appendChild(msgCell);
};

const onRoomClick = (e) => {
    let index = -1;
    // walk up the pointer event path until we find a room## id=
    const path = e.composedPath();
    for (let i = 0; i < path.length; i++) {
        if (path[i].id && path[i].id.startsWith("room")) {
            index = Number(path[i].id.substr("room".length));
            break;
        }
    }
    if (index === -1) {
        console.log("failed to find room for onclick");
        return;
    }
    // assign global state
    activeRoomId = rooms.roomIndexToRoomId[index];
    renderRoomContent(activeRoomId, true);
    render(document.getElementById("listContainer")); // get the highlight on the room
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
        document.getElementById("selectedroomavatar").src = mxcToUrl(room.avatar) || "/client/placeholder.svg";
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
}

const render = (container) => {
    let addCount = 0;
    let removeCount = 0;
    // ensure we have the right number of children, remove or add appropriately.
    while (container.childElementCount > rooms.joinedCount) {
        intersectionObserver.unobserve(container.firstChild);
        container.removeChild(container.firstChild);
        removeCount += 1;
    }
    for (let i = container.childElementCount; i < rooms.joinedCount; i++) {
        const template = document.getElementById("roomCellTemplate");
        // https://developer.mozilla.org/en-US/docs/Web/HTML/Element/template#avoiding_documentfragment_pitfall
        const roomCell = template.content.firstElementChild.cloneNode(true);
        roomCell.setAttribute("id", "room"+i);
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
        const roomId = rooms.roomIndexToRoomId[i];
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
            roomCell.getElementsByClassName("roomavatar")[0].src = mxcToUrl(r.avatar) || "/client/placeholder.svg";
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
            const mostRecentEvent = r.timeline[r.timeline.length-1];
            roomSenderSpan.textContent = mostRecentEvent.sender;
            roomTimestampSpan.textContent = formatTimestamp(mostRecentEvent.origin_server_ts);

            if (mostRecentEvent.type === "m.room.message") {
                roomContentSpan.textContent = mostRecentEvent.content.body;
            } else if (mostRecentEvent.type === "m.room.member") {
                roomContentSpan.textContent = "";
                roomSenderSpan.textContent = membershipChangeText(mostRecentEvent);
            } else if (mostRecentEvent.type) {
                roomContentSpan.textContent = mostRecentEvent.type + " event";
            }
        } else {
            roomContentSpan.textContent = "";
        }
    }
}
const sleep = (ms) => {
    return new Promise(resolve => setTimeout(resolve, ms));
}

const formatTimestamp = (originServerTs) => {
    const d = new Date(originServerTs);
    return (
        d.toDateString() + " " + zeroPad(d.getHours()) + ":" + zeroPad(d.getMinutes()) + ":" + zeroPad(d.getSeconds())
    );
}

const doSyncLoop = async(accessToken, sessionId) => {
    console.log("Starting sync loop. Active: ", activeSessionId, " this:", sessionId);
    let currentPos;
    let currentError = null;
    let currentSub = "";
    while (sessionId === activeSessionId) {
        let resp;
        try {
            // these fields are always required
            let reqBody = {
                rooms: activeRanges,
                session_id: (sessionId ? sessionId : undefined),
            };
            // if this is the first request on this session, send sticky request data which never changes
            if (!currentPos) {
                reqBody.required_state = requiredStateEventsInList;
            }
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
                    }
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
            if (resp.count) {
                rooms.joinedCount = resp.count;
            }
        } catch (err) {
            if (err.name !== "AbortError") {
                console.error("/sync failed:",err);
                console.log("current", currentError, "last", lastError);
                if (currentError != lastError) {
                    console.log("set!");
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
                resp.room_subscriptions[roomId], rooms.roomIdToRoom[roomId] !== undefined,
            );
            renderRoomContent(roomId);
        });

        let gapIndex = -1;
        resp.ops.forEach((op) => {
            if (op.op === "DELETE") {
                console.log("DELETE", op.index);
                delete rooms.roomIndexToRoomId[op.index];
                gapIndex = op.index;
            } else if (op.op === "INSERT") {
                console.log("INSERT", op.index);
                if (rooms.roomIndexToRoomId[op.index]) {
                    // something is in this space, shift items out of the way
                    if (gapIndex < 0) {
                        console.log("cannot work out where gap is, INSERT without previous DELETE!");
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
                        // starting at the gap so we can just shift each element in turn
                        // [A,B,C,C] i=3
                        // [A,B,B,C] i=2
                        // [A,A,B,C] i=1
                        // Terminate. We'll assign into op.index next.
                        for (let i = gapIndex; i > op.index; i--) {
                            rooms.roomIndexToRoomId[i] = rooms.roomIndexToRoomId[i-1];
                        }
                    } else if (gapIndex < op.index) {
                        // the gap is further up the list, shift every element to the left
                        // starting at the gap so we can just shift each element in turn
                        for (let i = gapIndex; i < op.index; i++) {
                            rooms.roomIndexToRoomId[i] = rooms.roomIndexToRoomId[i+1];
                        }
                    }
                }
                accumulateRoomData(op.room, rooms.roomIdToRoom[op.room.room_id] !== undefined);
                rooms.roomIndexToRoomId[op.index] = op.room.room_id;
                renderRoomContent(op.room.room_id);
            } else if (op.op === "UPDATE") {
                console.log("UPDATE", op.index);
                accumulateRoomData(op.room, true);
                renderRoomContent(op.room.room_id);
            } else if (op.op === "SYNC") {
                console.log("SYNC", JSON.stringify(op.range));
                const startIndex = op.range[0];
                for (let i = startIndex; i <= op.range[1]; i++) {
                    const r = op.rooms[i - startIndex];
                    if (!r) {
                        break; // we are at the end of list
                    }
                    rooms.roomIndexToRoomId[i] = r.room_id;
                    accumulateRoomData(r);
                }
            } else if (op.op === "INVALIDATE") {
                console.log("INVALIDATE", JSON.stringify(op.range));
                const startIndex = op.range[0];
                for (let i = startIndex; i <= op.range[1]; i++) {
                    delete rooms.roomIndexToRoomId[i];
                }
            }
        });
        render(document.getElementById("listContainer"));
    }
    console.log("active session: ", activeSessionId, " this session: ", sessionId, " terminating.");
}
// accessToken = string, pos = int, ranges = [2]int e.g [0,99]
const doSyncRequest = async (accessToken, pos, reqBody) => {
    activeAbortController = new AbortController();
    let resp = await fetch("/_matrix/client/v3/sync" + (pos ? "?pos=" + pos : ""), {
        signal: activeAbortController.signal,
        method: "POST",
        headers: {
            "Authorization": "Bearer " + accessToken,
            "Content-Type": "application/json",
        },
        body: JSON.stringify(reqBody)
    });
    let respBody = await resp.json();
    if (respBody.ops) {
        console.log(respBody);
    }
    if (resp.status != 200) {
        if (respBody.error) {
            lastError = respBody.error;
        }
        throw new Error("/sync returned HTTP " + resp.status + " " + respBody.error);
    }
    lastError = null;
    return respBody;
}

const membershipChangeText = (ev) => {
    const prevContent = (ev.unsigned || {}).prev_content || {};
    const prevMembership = prevContent.membership || "leave";
    const nowMembership = ev.content.membership;
    if (nowMembership != prevMembership) {
        switch (nowMembership) {
            case "join":
                return ev.state_key + " joined the room";
            case "leave":
                return ev.state_key + " left the room";
            case "ban":
                return ev.sender + " banned " + ev.state_key + " from the room";
            case "invite":
                return ev.sender + " invited " + ev.state_key + " to the room";
            case "knock":
                return ev.state_key + " knocked on the room";
        }
    }
    if (nowMembership == prevMembership && nowMembership == "join") {
        // display name or avatar change
        if (prevContent.displayname !== ev.content.displayname) {
            return ev.state_key + " set their name to " + ev.content.displayname;
        }
        if (prevContent.avatar_url !== ev.content.avatar_url) {
            return ev.state_key + " changed their profile picture";
        }
    }
    return ev.type + " event";
}

const randomName = (i, long) => {
    if (i % 17 === 0) {
        return long ? "Ever have that feeling where you’re not sure if you’re awake or dreaming?" : "There is no spoon";
    } else if (i % 13 === 0) {
        return long ? "Choice is an illusion created between those with power and those without." : "Get Up Trinity";
    } else if (i % 11 === 0) {
        return long ? "That’s how it is with people. Nobody cares how it works as long as it works.": "I know kung fu";
    } else if (i % 7 === 0) {
        return long ? "The body cannot live without the mind." : "Free your mind";
    } else if (i % 5 === 0) {
        return long ? "Perhaps we are asking the wrong questions…" : "Agent Smith";
    } else if (i % 3 === 0) {
        return long ? "You've been living in a dream world, Neo." : "Mr Anderson";
    } else {
        return long ? "Mr. Wizard, get me the hell out of here! " : "Morpheus";
    }
}

const zeroPad = (n) => {
    if (n < 10) {
        return "0" + n;
    }
    return n;
}

const mxcToUrl = (mxc) => {
    const path = mxc.substr("mxc://".length);
    if (!path) {
        return;
    }
    // TODO: we should really use the proxy HS not matrix.org
    return `https://matrix-client.matrix.org/_matrix/media/r0/thumbnail/${path}?width=64&height=64&method=crop`;
}

window.addEventListener('load', (event) => {
    const storedAccessToken = window.localStorage.getItem("accessToken");
    if (storedAccessToken) {
        document.getElementById("accessToken").value = storedAccessToken;
    }
    document.getElementById("syncButton").onclick = () => {
        const accessToken = document.getElementById("accessToken").value;
        window.localStorage.setItem("accessToken", accessToken);
        activeSessionId = new Date().getTime() + "";
        doSyncLoop(accessToken, activeSessionId);
    }
});