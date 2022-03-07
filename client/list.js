/**
 * This file contains code for the room list DOM view. It controls rendering elements, the IntersectionObserver
 * and lazy loading DOM nodes off-screen to ensure the list doesn't take O(n) to load.
 */

export class List {
    /**
     * Construct a new List. Children in this list MUST have `id=` attributes which contain sequential numbers.
     * @param {string} idPrefix The prefix to remove from the `id=` attribute of children, if any.
     * @param {number} debounceTime The number of milliseconds to wait before invoking the callback on intersection change.
     * @param {function} callback The callback that will be invoked when the intersection has changed. It is called with
     * args for the startIndex, endIndex (inclusive).
     */
    constructor(idPrefix, debounceTime, callback) {
        this.debounceTimeoutId = null;
        this.debounceTime = debounceTime;
        this.idPrefix = idPrefix;
        this.visibleIndexes = {}; // e.g "44" meaning index 44

        this.intersectionObserver = new IntersectionObserver(
            (entries) => {
                entries.forEach((entry) => {
                    let key = entry.target.id.substring(this.idPrefix.length);
                    if (entry.isIntersecting) {
                        this.visibleIndexes[key] = true;
                    } else {
                        delete this.visibleIndexes[key];
                    }
                });
                // we will process the intersections after a short period of inactivity to not thrash the server
                clearTimeout(this.debounceTimeoutId);
                this.debounceTimeoutId = setTimeout(() => {
                    let startIndex = -1;
                    let endIndex = -1;
                    Object.keys(this.visibleIndexes).forEach((roomIndex) => {
                        // e.g "44"
                        let i = Number(roomIndex);
                        if (startIndex === -1 || i < startIndex) {
                            startIndex = i;
                        }
                        if (endIndex === -1 || i > endIndex) {
                            endIndex = i;
                        }
                    });
                    callback(startIndex, endIndex);
                }, this.debounceTime);
            },
            {
                threshold: [0],
            }
        );
    }

    resize(container, count, createElement) {
        let addCount = 0;
        let removeCount = 0;
        // ensure we have the right number of children, remove or add appropriately.
        while (container.childElementCount > count) {
            this.intersectionObserver.unobserve(container.lastChild);
            container.removeChild(container.lastChild);
            removeCount += 1;
        }
        for (let i = container.childElementCount; i < count; i++) {
            const cell = createElement(i);
            container.appendChild(cell);
            this.intersectionObserver.observe(cell);
            addCount += 1;
        }
        if (addCount > 0 || removeCount > 0) {
            console.log(
                "resize: added ",
                addCount,
                "nodes, removed",
                removeCount,
                "nodes"
            );
        }
    }
}
