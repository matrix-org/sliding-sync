# Sliding Sync Landing Page

Welcome intrepid Matrix explorer. You've landed on this page because you're trying to use a next generation client that requires your homeserver supports a new and experimental feature called sliding sync which enables lightning fast loading of your messages. In order to continue with this client, you'll need to ask your server admin to do the following:

1. Configure Postgres and install a sliding sync proxy as described in the [docs](https://github.com/matrix-org/sliding-sync).
2. Advertise the proxy by configuring an [`org.matrix.msc3575.proxy`](https://github.com/matrix-org/matrix-spec-proposals/blob/kegan/sync-v3/proposals/3575-sync.md#unstable-prefix) property in the server's `/.well-known/matrix/client` file (see [Synapse docs](https://element-hq.github.io/synapse/latest/setup/installation.html#client-well-known-uri)):
   ```json
   {
       "org.matrix.msc3575.proxy": {
           "url": "https://slidingsync.proxy.url.here"
       }
   }
   ```

   If you are using Synapse's built-in support for `/.well-known/matrix/client` (i.e., you have configured your reverse proxy to forward `https://<server_name>/.well-known/matrix/client` requests to Synapse), you can do this by adding the following to your `homeserver.yaml`:

   ```yaml
   extra_well_known_client_content:
     "org.matrix.msc3575.proxy":
       "url": "https://slidingsync.proxy.url.here"
   ```

Once these steps are complete you will be able to continue on your journey of exploring the very latest that Matrix has to offer!
