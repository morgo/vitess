A braindump of ideas to work on before publishing...

vttablet:

- For most requests to a tablet, I've extended gRPC to add "Dbnameoverride" or similar. I think it's better that the input be target (keyspace, shard_id) and the registry then converts that to a dbname override.
- Ensure that vttablet subcomponents don't use the DB connection methods WithDB(). Everything should be using explicit DB names to prevent stray routing. I know there's at least one use DB call we need to remove and rewrite instead too.
- In vreplication, there's a hardcoded case of specifying the target keyspace. Need to fix it to read from the correct location (may need to extend metadata?). This will be a blocker to adding more tests.

vtgate, query serving etc:
- Need to naturally update the routing to vttablet as we change from dbNameOverride to "target".
- There's some code in discovery where it waits for certain tablets to be ready. This can probably be reverted, since it filtered out virtual keyspaces, but it can just filter out virtual tablets, which it probably will automatically.

Protobufs:
- I extended protobufs in a very POC way. Most of these changes were to add a dbNameOverride, but some cases have both a dbNameOverride and a target which is silly. Most of this can be reverted. Where required, we can standardize on target (see vttablet above).


Not planned:
- Converting physical shards to virtual.
- Local (virtual) shard splits.
- Moving virtual shard (same logistics but different from move tables; moves the whole keyspace to a different physical shard and updates the registry metadata on tablets).

