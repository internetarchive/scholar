# elasticsearch

Scholar and fatcat's searches are powered by a single 7.x Elasticsearch cluster.

## Topology

We have four nodes:

- `wbgrp-svc510`, a data-less master node
- `wbgrp-svc500`, a data node that can become master if needed
- `wbgrp-svc503`, a data node that _cannot_ become master
- `wbgrp-svc097`, a data node that can be come master if needed

These are managed via the `scholar-es7` role over in `ait-ansible`.

## Indices

To learn about how the indices are shaped, use:

```
curl -s localhost:9200/INDEX_NAME/_mapping | jq | less
```

- `scholar_fulltext`
  - The big one. It's a few TB and has all the fulltext we've extracted from PDFs. What you search when you search on scholar.archive.org.
- `fatcat_container`
  - Metadata on containers. A search for this is exposed on scholar.archive.org/fatcat.
- `fatcat_file`
  - Metadata on files. A search for this is exposed on scholar.archive.org/fatcat.
- `fatcat_release`
  - Metadata on releases. A search for this is exposed on scholar.archive.org/fatcat.
- `fatcat_changelog`
  - An index of the edits that used to be tracked for the old version of
    fatcat. This index is not written to anymore and can likely be deleted if
    it serves no use to posterity.
- `fatcat_ref`
  - An index built by [Martin](./people.md) during research on citation graphs.
    It can be used to examine references in to and out from a release in the
    fatcat web UI. However, it's not clear to me that it's being written to
    live and may be stale since Martin's departure.

There are also QA versions of some of the above indices:

- `qa_scholar_fulltext`
- `qa_fatcat_container`
- `qa_fatcat_file`
- `qa_fatcat_release`

These aren't actively used by anything and contain little but are here if you
want to test ingestion or test out potentially expensive search queries.

## Metrics

`svc510` runs `elasticsearch_exporter`. This scrapes metrics from all four
nodes and makes it available to prometheus (which in turn is queryable from
grafana).

## Maintenance

As of writing we don't have good observability on the cluster. If it goes down
you will not get pinged in any capacity. Keep an eye on the scholar Grafana
dashboard; it graphs disk i/o for the cluster's nodes. Should any nodes other
than 510 show no disk activity that's bad and implies a downed node. The
master-only node never really needs to touch its disk so it'll always report
low i/o.

In theory, downed nodes will restart themselves and the cluster will self-heal
and rebalance as needed. Use:

```bash
curl https://scholar.archive.org/_es/_cat/health
```

to check on cluster health. It'll show yellow if things are moving around;
green if everything is ok; and red if you have to manually intervene. If
rebalancing is simply not working or a node is stuck in a restart loop, try to
bring the entire cluster down (running `systemctl stop elasticsearch` on each
node) then back up again, starting with 510.

In general, the `_cat` family of commands is _extremely_ useful when debugging
elasticsearch; run `_cat` with no additional subpath to see all that it can do.

## Snapshots

Snapshots are incremental and give us the ability to restore the cluster if
something goes wrong with the live indices. They take some labor to perform but
are generally low drama.

First, verify the snapshot directory; it should already exist and have a
snapshot in it. If it doesn't then you know more than I do about what's going
on...

```
ls /kubwa/elasticsearch-snapshots
```

Verify that aitio has the NFS port open in `/etc/ferm/input/nfs`:

```
saddr $CLUSTER proto tcp dport 2049 ACCEPT;
```


Ensure that the NFS exports are enabled on aitio in `/etc/exports`:

```
/kubwa/elasticsearch-snapshots/ wbgrp-svc097.us.archive.org(rw,sync,no_subtree_check,all_squash)
/kubwa/elasticsearch-snapshots/ wbgrp-svc500.us.archive.org(rw,sync,no_subtree_check,all_squash)
/kubwa/elasticsearch-snapshots/ wbgrp-svc503.us.archive.org(rw,sync,no_subtree_check,all_squash)
/kubwa/elasticsearch-snapshots/ wbgrp-svc510.us.archive.org(rw,sync,no_subtree_check,all_squash)
```

then run:

```
sudo exportfs -ra
```

On the ES nodes, ensure that the snapshots path exists:

```
sudo mkdir -p /mnt/elasticsearch-snapshots/ && sudo chown elasticsearch:staff /mnt/elasticsearch-snapshots/
```

Then mount nfs on each node:

```
sudo mount -t nfs aitio.us.archive.org:/magna/elasticsearch-snapshots /mnt/elasticsearch-snapshots
```

As of writing, the nodes are `svc500`, `svc510`, `svc503`, and `svc097`.

Elasticsearch stores snapshots in a "repository." Repositories have names are
are the mechanism by which snapshots can be incremental. Ours is called
`backup`. When requesting a snapshot you can give it whatever name; I suggest
`snapshot_YYYYMMDD` updating the timestamp as needed.

To start the snapshot, I recommend running this command on the data-less master `svc510` within `tmux`:

```
curl -X PUT localhost:9200/_snapshot/backup/snapshot_20260709
```

You should get `{"accepted":true}` back.

Check on the status of the snapshot with:

```
curl -sX GET "localhost:9200/_snapshot/backup/_current?pretty"
```

You can filter to just the state by piping to `jq -r .snapshots[0].state`.

It shouldn't take _super_ long for this nor need a ton of disk space but expect at least a few hours and a few hundred GB. It scales with how long since the last snapshot, of course.

I recommend something like:

```
watch 'curl -s localhost:9200/_cat/snapshots/backup'
```

to watch your snapshot become reality.
