# Log line format

Log lines may contain the following fields:

 * `u`: user ID.
 * `dev`: device ID.
 * `p`: `pos` parameter from incoming requests.
 * `q`: `pos` value from outgoing responses.
 * `t`: transaction ID.
 * `c`: connection ID.
 * `b`: buffer summary.
 * Update counts in outgoing responses, each of which are omitted if zero:
   * `r`: rooms.
   * `d`: to-device events.
   * `ag`: global account data updates.
   * `dl-c`: changed devices.
   * `dl-l`: left devices.
   * `sub`: room subscriptions.
   * `usub`: room unsubscriptions.
   * `l`: lists.
