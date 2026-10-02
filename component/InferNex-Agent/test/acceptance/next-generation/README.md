# Next-generation acceptance starter kit

This directory is a bounded, CPU-only development fixture for the proposal in
[`next-generation-topic-and-acceptance-zh.md`](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/architecture/next-generation-topic-and-acceptance-zh.md).
It does not constitute the full acceptance delivery.

The committed dataset is deterministic and explicitly synthetic. It contains no
real HCCL plog, cluster capture, benchmark result, or proof of a real diagnosis.
Participant data and evaluator truth use separate directory trees, but a public
repository is not an access boundary. Formal blind evaluation must place truth
in a separate evaluator account/environment and use unpublished variants.

From this directory, run the local checks without Docker or a cluster:

```bash
python3 selfcheck.py
fresh_dir="$(mktemp -d)"
python3 generate.py --output "${fresh_dir}/generated"
diff -ru generated "${fresh_dir}/generated"
```

The generator refuses an existing output path. See the
[Chinese lab guide](https://github.com/lsjfy-open-com/infernex-agent/blob/develop/component/InferNex-Agent/docs/guides/next-generation-acceptance-lab-zh.md)
for the optional Kind lab, offline preparation, cleanup, evidence intake, and
remaining gaps.

