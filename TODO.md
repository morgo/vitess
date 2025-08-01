A braindump of ideas to work on before publishing...

vttablet:

- When a tablet server starts up, it should have a component called a registry. The registry queries the topo and figures out what targets should be served for this tablet. The registry is then provided as input to other components.
- For most requests to a tablet, I've extended gRPC to add "Dbnameoverride" or similar. I think it's better that the input be target (keyspace, shard_id) and the registry then converts that to a dbname override.
- For backward compatibility (so a newer vttablet can still server requests from older other components) the target provided to the registry can be nil. When there's only the one physical tablet, nil returns it. If there's more than one there's an error saying upgrade your other components.
- Ensure that vttablet subcomponents don't use the DB connection methods WithDB(). Everything should be using explicit DB names to prevent stray routing. I know there's at least one use DB call we need to remove and rewrite instead too.
- In vreplication, there's a hardcoded case of specifying the target keyspace. Need to fix it to read from the correct location (may need to extend metadata?). This will be a blocker to adding more tests.

vtgate, query serving etc:
- Need to naturally update the routing to vttablet as we change from dbNameOverride to "target".
- There's some code in discovery where it waits for certain tablets to be ready. This can probably be reverted, since it filtered out virtual keyspaces, but it can just filter out virtual tablets, which it probably will automatically.

Protobufs:
- I extended protobufs in a very POC way. Most of these changes were to add a dbNameOverride, but some cases have both a dbNameOverride and a target which is silly. Most of this can be reverted. Where required, we can standardize on target (see vttablet above).

- examples:
- We should just create one physical "main" and have all the activity be on it.

Not planned:
- Converting physical shards to virtual.
- Local (virtual) shard splits.
- Moving virtual shard (same logistics but different from move tables; moves the whole keyspace to a different physical shard and updates the registry metadata on tablets).




Remove hard coded strings:


Critical:

+++ b/go/vt/vtctl/workflow/server.go
+       // For virtual shards, we need to determine the correct database name override
+       // TODO: do the correct dbNameOverride for a virtual shard here.
+       // The challenge with this, is that we do not know the shard
+       // that will be used for the workflow, so we cannot use the shard name.
+       dbNameOverride := "vt_" + targetKeyspace + "_0"


Not so critical yet:

+++ b/go/vt/vtctl/workflow/utils.go
+               if targetKeyspace != primary.Keyspace {
+                       // This is a virtual shard - use the virtual keyspace database name
+                       dbName = fmt.Sprintf("vt_%s_%s", targetKeyspace, targetShard)
+               }

+++ b/go/vt/vtctl/workflow/workflows.go
+               // Set the DbNameOverride for virtual shards
+               // If the requested keyspace differs from the tablet's physical keyspace,
+               // this indicates we're dealing with a virtual shard and need to override
+               // the database name to query the correct VReplication data.
+               if req.Keyspace != primary.Keyspace {
+                       // For virtual shards, construct the database name using the virtual keyspace
+                       reqClone.DbNameOverride = fmt.Sprintf("vt_%s_%s", req.Keyspace, si.ShardName())
+               }
+

