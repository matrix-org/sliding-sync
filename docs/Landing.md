# Sliding Sync Landing Page

Welcome intrepid Matrix explorer. You've landed on this page because you're trying to use a next generation client that requires your homeserver supports a new and experimental feature called sliding sync which enables lightning fast loading of your messages. In order to continue with this client, you'll need to ask your server admin to do the following:

- Configure Postgres and install a sliding sync proxy as described in the [docs](https://github.com/matrix-org/sliding-sync).
- Advertise the proxy by configuring an [`org.matrix.msc3575.proxy`](https://github.com/matrix-org/matrix-spec-proposals/blob/kegan/sync-v3/proposals/3575-sync.md#unstable-prefix) property in the server's `/.well-known/matrix/client` config:
```
{
    "org.matrix.msc3575.proxy": {
        "url": "https://slidingsync.proxy.url.here"
    }
}
```

Once these steps are complete you will be able to continue on your journey of exploring the very latest that Matrix has to offer!
