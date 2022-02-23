// This file contains code to map data structures to actual HTML elements.
// It is mostly functional and boring and it does not include any sliding sync specific data.
// In other words, if you want to learn about sliding sync, this isn't the file to look at.

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
};

// TODO: remove export
export const textForEvent = (ev) => {
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

const zeroPad = (n) => {
    if (n < 10) {
        return "0" + n;
    }
    return n;
};

// TODO: remove export
export const formatTimestamp = (originServerTs) => {
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

export const renderEvent = (eventIdKey, ev) => {
    const template = document.getElementById("messagetemplate");
    // https://developer.mozilla.org/en-US/docs/Web/HTML/Element/template#avoiding_documentfragment_pitfall
    const msgCell = template.content.firstElementChild.cloneNode(true);
    msgCell.setAttribute("id", eventIdKey);
    msgCell.getElementsByClassName("msgsender")[0].textContent = ev.sender;
    msgCell.getElementsByClassName("msgtimestamp")[0].textContent = formatTimestamp(
        ev.origin_server_ts
    );
    let body = textForEvent(ev);
    msgCell.getElementsByClassName("msgcontent")[0].textContent = body;
    return msgCell;
}

