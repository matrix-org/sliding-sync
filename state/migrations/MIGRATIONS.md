# Database migrations in the Sliding Sync Proxy

Database migrations are using https://github.com/pressly/goose, with an integrated `migrate` command in the `syncv3` binary.

All commands below require the `SYNCV3_DB` environment variable to determine which database to use:

```bash
$ export SYNCV3_DB="user=postgres dbname=syncv3 sslmode=disable password=yourpassword"
```

## Upgrading

It is sufficient to run the proxy itself, upgrading is done automatically. If you still have the need to upgrade manually, you can
use one of the following commands to upgrade:

```bash
# Check which versions have been applied
$ ./syncv3 migrate status

# Execute all existing migrations
$ ./syncv3 migrate up

# Upgrade to 20230802121023 (which is 20230802121023_device_data_jsonb.go - migrating from bytea to jsonb)
$ ./sync3 migrate up-to 20230728114555

# Upgrade by one
$ ./syncv3 migrate up-by-one
```

## Downgrading

If you wish to downgrade, executing one of the following commands. If you downgrade, make sure you also start
the required older version of the proxy, as otherwise the schema will automatically be upgraded again:

```bash
# Check which versions have been applied
$ ./syncv3 migrate status

# Undo the latest migration
$ ./syncv3 migrate down

# Downgrade to 20230728114555 (which is 20230728114555_device_data_drop_id.sql - dropping the id column)
$ ./sync3 migrate down-to 20230728114555
```

## Creating new migrations

Migrations can either be created as plain SQL or as Go functions.

```bash
# Create a new SQL migration with the name "mymigration"
$ ./syncv3 migrate create mymigration sql 

# Same as above, but as Go functions
$ ./syncv3 migrate create mymigration go
```