/*
 * This file contains code to map data structures to actual HTML elements.
 * It is mostly functional and boring and it does not include any sliding sync specific data.
 * In other words, if you want to learn about sliding sync, this isn't the file to look at.
 */

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
            return (
                ev.state_key + " set their name to " + ev.content.displayname
            );
        }
        if (prevContent.avatar_url !== ev.content.avatar_url) {
            return ev.state_key + " changed their profile picture";
        }
    }
    return ev.type + " event";
};

const textForEvent = (ev) => {
    let body = "";
    switch (ev.type) {
        case "m.room.message":
            body = ev.content.body;
            break;
        case "m.room.member":
            body = membershipChangeText(ev);
            break;
        case "m.reaction":
            body = "reacted with " + (ev.content["m.relates_to"] || {}).key;
            break;
        default:
            body = ev.type + " event";
            break;
    }
    return body;
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
        return long
            ? "The body cannot live without the mind."
            : "Free your mind";
    } else if (i % 5 === 0) {
        return long
            ? "Perhaps we are asking the wrong questions…"
            : "Agent Smith";
    } else if (i % 3 === 0) {
        return long
            ? "You've been living in a dream world, Neo."
            : "Mr Anderson";
    } else {
        return long ? "Mr. Wizard, get me the hell out of here! " : "Morpheus";
    }
};

const roomIdAttr = (listIndex, roomIndex) => {
    return "room-" + listIndex + "-" + roomIndex;
};

const zeroPad = (n) => {
    if (n < 10) {
        return "0" + n;
    }
    return n;
};

const formatTimestamp = (originServerTs) => {
    const d = new Date(originServerTs);
    return (
        d.toDateString() +
        " " +
        zeroPad(d.getHours()) +
        ":" +
        zeroPad(d.getMinutes()) +
        ":" +
        zeroPad(d.getSeconds())
    );
};

export const mxcToUrl = (syncv2ServerUrl, mxc) => {
    const path = mxc.substr("mxc://".length);
    if (!path) {
        return;
    }
    return `${syncv2ServerUrl}/_matrix/media/r0/thumbnail/${path}?width=64&height=64&method=crop`;
};

export const renderEvent = (eventIdKey, ev) => {
    const template = document.getElementById("messagetemplate");
    // https://developer.mozilla.org/en-US/docs/Web/HTML/Element/template#avoiding_documentfragment_pitfall
    const msgCell = template.content.firstElementChild.cloneNode(true);
    msgCell.setAttribute("id", eventIdKey);
    msgCell.getElementsByClassName("msgsender")[0].textContent = ev.sender;
    msgCell.getElementsByClassName("msgtimestamp")[0].textContent =
        formatTimestamp(ev.origin_server_ts);
    let body = textForEvent(ev);
    msgCell.getElementsByClassName("msgcontent")[0].textContent = body;
    return msgCell;
};

/**
 * Render the room list in the given `container`. This is read-only and does not modify internal data structures.
 * @param {Element} container The DOM node to attach the list to.
 * @param {string} syncv2ServerUrl The CS API URL. Used for <img> tags for room avatars.
 * @param {Number} listIndex The index position of this list. Used for setting non-clashing ID attributes.
 * @param {SlidingList} slidingList The list to render.
 * @param {string} highlightedRoomID The room being viewed currently.
 * @param {object} roomIdToRoom  The data store containing the room data.
 * @param {IntersectionObserver} intersectionObserver The intersection observer for observing scroll positions.
 * @param {function} onRoomClick The DOM callback to invoke when a room cell is clicked.
 */
export const renderRoomList = (
    container,
    syncv2ServerUrl,
    listIndex,
    slidingList,
    highlightedRoomID,
    roomIdToRoom,
    intersectionObserver,
    onRoomClick
) => {
    let addCount = 0;
    let removeCount = 0;
    // ensure we have the right number of children, remove or add appropriately.
    while (container.childElementCount > slidingList.joinedCount) {
        intersectionObserver.unobserve(container.lastChild);
        container.removeChild(container.lastChild);
        removeCount += 1;
    }
    for (
        let i = container.childElementCount;
        i < slidingList.joinedCount;
        i++
    ) {
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
        console.log(
            "render: added ",
            addCount,
            "nodes, removed",
            removeCount,
            "nodes"
        );
    }
    // loop all elements and modify the contents
    for (let i = 0; i < container.children.length; i++) {
        const roomCell = container.children[i];
        const roomId = slidingList.roomIndexToRoomId[i];
        const r = roomIdToRoom[roomId];
        // if this child is a placeholder and it was previously a placeholder then do nothing.
        if (!r && roomCell.getAttribute("x-placeholder") === "yep") {
            continue;
        }
        const roomNameSpan = roomCell.getElementsByClassName("roomname")[0];
        const roomContentSpan =
            roomCell.getElementsByClassName("roomcontent")[0];
        const roomSenderSpan = roomCell.getElementsByClassName("roomsender")[0];
        const roomTimestampSpan =
            roomCell.getElementsByClassName("roomtimestamp")[0];
        const unreadCountSpan =
            roomCell.getElementsByClassName("unreadcount")[0];
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
            roomCell.getElementsByClassName("roomavatar")[0].src =
                "/client/placeholder.svg";
            roomCell.style = "";
            roomCell.setAttribute("x-placeholder", "yep");
            continue;
        }
        roomCell.removeAttribute("x-placeholder");
        roomCell.style = "";
        roomNameSpan.textContent = r.name || r.room_id;
        roomNameSpan.style = "";
        roomContentSpan.style = "";
        if (r.avatar) {
            roomCell.getElementsByClassName("roomavatar")[0].src =
                mxcToUrl(syncv2ServerUrl, r.avatar) ||
                "/client/placeholder.svg";
        } else {
            roomCell.getElementsByClassName("roomavatar")[0].src =
                "/client/placeholder.svg";
        }
        if (roomId === highlightedRoomID) {
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
            roomTimestampSpan.textContent = formatTimestamp(
                mostRecentEvent.origin_server_ts
            );

            const body = textForEvent(mostRecentEvent);
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
