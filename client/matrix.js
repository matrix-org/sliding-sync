/**
 * This file contains Matrix CS API code. Not relevant for sliding sync, but relevant for general
 * client code e.g sending events.
 */

async function doRequest(fullUrl, accessToken, method, body) {
    const resp = await fetch(fullUrl, {
        method: method,
        headers: {
            Authorization: "Bearer " + accessToken,
            "Content-Type": "application/json",
        },
        body: body ? JSON.stringify(body) : undefined,
    });
    if (!resp.ok) {
        throw new Error("HTTP " + resp.status);
    }
    return await resp.json();
}

async function inviteToRoom(v2serverUrl, accessToken, roomId, userId) {
    await doRequest(
        `${v2serverUrl}/_matrix/client/v3/rooms/${encodeURIComponent(
            roomId
        )}/invite`,
        accessToken,
        "POST",
        {
            user_id: userId,
        }
    );
}

async function joinRoom(v2serverUrl, accessToken, roomIdOrAlias) {
    await doRequest(
        `${v2serverUrl}/_matrix/client/v3/join/${encodeURIComponent(
            roomIdOrAlias
        )}`,
        accessToken,
        "POST",
        {}
    );
}

async function leaveRoom(v2serverUrl, accessToken, roomId) {
    await doRequest(
        `${v2serverUrl}/_matrix/client/v3/rooms/${encodeURIComponent(
            roomId
        )}/leave`,
        accessToken,
        "POST",
        {}
    );
}

/**
 * Send a Matrix event.
 * @param {string} v2serverUrl The CS API endpoint base URL.
 * @param {string} accessToken The user's access token.
 * @param {string} roomId The room to send the event in.
 * @param {string} rawText The raw text input by the user. The text entered will affect the operation.
 */
export async function sendMessage(v2serverUrl, accessToken, roomId, rawText) {
    rawText = rawText.trim();

    if (rawText.startsWith("/invite ")) {
        await inviteToRoom(
            v2serverUrl,
            accessToken,
            roomId,
            rawText.substring("/invite ".length)
        );
        return;
    } else if (rawText.startsWith("/join ")) {
        await joinRoom(
            v2serverUrl,
            accessToken,
            rawText.substring("/join ".length)
        );
        return;
    } else if (rawText === "/leave") {
        await leaveRoom(v2serverUrl, accessToken, roomId);
        return;
    }

    let jsonBody = {
        msgtype: "m.text",
        body: rawText,
    };
    if (rawText.startsWith("/me ")) {
        jsonBody = {
            msgtype: "m.emote",
            body: rawText.substring("/me ".length),
        };
    }
    const txnId = "" + new Date().getTime();

    await doRequest(
        `${v2serverUrl}/_matrix/client/v3/rooms/${encodeURIComponent(
            roomId
        )}/send/m.room.message/${txnId}`,
        accessToken,
        "PUT",
        jsonBody
    );
}
