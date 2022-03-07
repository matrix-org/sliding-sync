/*
 * This file contains code to render the developer tools overlay (bandwidth, list visualisations, etc)
 * You don't need to read this file to understand sliding sync.
 */

/**
 * Set the bandwidth values on the devtools display.
 * @param {Element} domNode The node to insert the bandwidth stats to.
 * @param {SlidingSyncConnection} conn The sliding sync connection
 */
export function bandwidth(domNode, conn) {
    domNode.textContent =
        (conn.txBytes / 1024.0).toFixed(2) +
        " KB Tx / " +
        (conn.rxBytes / 1024.0).toFixed(2) +
        " KB Rx";
}

/**
 * Generate an SVG visualisation of the sliding lists and attaches it as a child to the domNode provided.
 * Does nothing if there are no resp.ops.
 * @param {Element} domNode The node to insert the SVG into.
 * @param {[]SlidingList} activeLists The lists
 * @param {object} resp The Sliding Sync response JSON
 */
export function svgify(domNode, activeLists, resp) {
    if (resp.ops.length === 0) {
        return;
    }
    const horizontalPixelWidth = 10;
    let verticalPixelHeight = 1;
    // Remove any previous SVG
    while (domNode.firstChild) {
        domNode.removeChild(domNode.firstChild);
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
    });
    if (height < window.innerHeight / 2) {
        // we can double the vertical pixel height to make it easier to see
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
        const placeholders = document.createElementNS(
            "http://www.w3.org/2000/svg",
            "rect"
        );
        placeholders.setAttribute("x", index * 2 * horizontalPixelWidth);
        placeholders.setAttribute("y", 0);
        placeholders.setAttribute("width", horizontalPixelWidth);
        placeholders.setAttribute(
            "height",
            al.joinedCount * verticalPixelHeight
        );
        placeholders.setAttribute("fill", colorPlaceholder);

        svg.appendChild(placeholders);
        // [[0, 20], [50,60]];
        al.activeRanges.forEach((range) => {
            const rect = document.createElementNS(
                "http://www.w3.org/2000/svg",
                "rect"
            );
            rect.setAttribute("x", index * 2 * horizontalPixelWidth);
            rect.setAttribute("y", range[0] * verticalPixelHeight);
            rect.setAttribute("width", horizontalPixelWidth);
            rect.setAttribute(
                "height",
                (range[1] - range[0]) * verticalPixelHeight
            );
            rect.setAttribute("fill", colorInWindow);
            svg.appendChild(rect);
        });
    });

    const addLine = (index, y, colour, yLen) => {
        const bar = document.createElementNS(
            "http://www.w3.org/2000/svg",
            "rect"
        );
        bar.setAttribute("x", index * 2 * horizontalPixelWidth);
        bar.setAttribute("y", y * verticalPixelHeight);
        bar.setAttribute("width", horizontalPixelWidth);
        bar.setAttribute("height", verticalPixelHeight * (yLen ? yLen : 1));
        bar.setAttribute("fill", colour);
        const animation = document.createElementNS(
            "http://www.w3.org/2000/svg",
            "animate"
        );
        animation.setAttribute("attributeName", "visibility");
        animation.setAttribute("from", "visible");
        animation.setAttribute("to", "hidden");
        animation.setAttribute("dur", "0.5s");
        animation.setAttribute("repeatCount", "3");
        bar.appendChild(animation);
        svg.appendChild(bar);
    };

    // add insertions, deletions and updates
    resp.ops.forEach((op) => {
        if (op.op === "DELETE") {
            addLine(op.list, op.index, colorDelete);
        } else if (op.op === "INSERT") {
            addLine(op.list, op.index, colorInsert);
        } else if (op.op === "UPDATE") {
            addLine(op.list, op.index, colorUpdate);
        } else if (op.op === "SYNC") {
            addLine(
                op.list,
                op.range[0],
                colorSync,
                op.range[1] - op.range[0] + 1
            );
        } else if (op.op === "INVALIDATE") {
            addLine(
                op.list,
                op.range[0],
                colorInvalidate,
                op.range[1] - op.range[0] + 1
            );
        }
    });

    // this is expensive so only do it on smaller accounts
    if (height < 500) {
        const fifth = horizontalPixelWidth / 5;
        // draw white dot for each room which has some kind of data stored
        activeLists.forEach((al, index) => {
            for (let roomIndex of Object.keys(al.roomIndexToRoomId)) {
                const roomPixel = document.createElementNS(
                    "http://www.w3.org/2000/svg",
                    "rect"
                );
                roomPixel.setAttribute(
                    "x",
                    index * 2 * horizontalPixelWidth + fifth
                );
                roomPixel.setAttribute("y", roomIndex * verticalPixelHeight);
                roomPixel.setAttribute("width", fifth);
                roomPixel.setAttribute("height", verticalPixelHeight);
                roomPixel.setAttribute("fill", colorRoom);
                svg.appendChild(roomPixel);
            }
        });
    }

    /* Move animations, maybe bring this back?
    const animation = document.createElementNS("http://www.w3.org/2000/svg","animate");
    animation.setAttribute("attributeName", "y");
    animation.setAttribute("from", al.joinedCount);
    animation.setAttribute("to", 0);
    animation.setAttribute("dur", "1s");
    animation.setAttribute("fill", "freeze");
    deleteBar.appendChild(animation); */

    // insert the SVG
    domNode.appendChild(svg);
}
