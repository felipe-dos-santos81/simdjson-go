"""Prints run.sh's Markdown table from benchstat's CSV on stdin
(benchstat -table pkg -format csv -filter .unit:B/s C++ Go): the median
throughput of each benchmark on both sides and Go / C++, then the geometric
mean of the ratios. Exits 1 if a benchmark has a result on one side only.

python3 -I table.py --check runs it on a recorded sample."""

import csv
import math
import re
import sys


def table(lines):
    rows, unpaired = [], []
    for rec in csv.reader(lines):
        if len(rec) < 2 or rec[0] in ("", "geomean"):
            continue  # "pkg: …", headers, blank lines, benchstat's own geomean
        name = re.sub(r"-\d+$", "", rec[0])
        cpp, go = rec[1], rec[3] if len(rec) > 3 else ""
        if not cpp or not go:
            unpaired.append(name)
            continue
        rows.append((name, float(cpp), float(go)))
    if unpaired:
        raise ValueError("on one side only: " + ", ".join(unpaired))
    out = ["| Benchmark | C++ | Go | Go / C++ |", "|---|---|---|---|"]
    for name, cpp, go in rows:
        out.append(f"| `{name}` | {cpp / 2**20:.0f} MiB/s | {go / 2**20:.0f} MiB/s | {go / cpp:.2f}× |")
    geomean = math.exp(sum(math.log(go / cpp) for _, cpp, go in rows) / len(rows))
    out.append(f"| geomean | | | {geomean:.2f}× |")
    return "\n".join(out) + "\n"


SAMPLE = """pkg: simdjson-go
,C++,,Go,,,
,B/s,CI,B/s,CI,vs base,P
Parse/twitter.json-14,4.194304e+09,1%,1.048576e+09,0%,-75.00%,p=0.002 n=6
geomean,4.194304e+09,,1.048576e+09,,-75.00%,
"""


def check():
    want = (
        "| Benchmark | C++ | Go | Go / C++ |\n|---|---|---|---|\n"
        "| `Parse/twitter.json` | 4000 MiB/s | 1000 MiB/s | 0.25× |\n"
        "| geomean | | | 0.25× |\n"
    )
    got = table(SAMPLE.splitlines())
    assert got == want, got
    try:
        table((SAMPLE + "Parse/canada.json-14,,,9e+06,1%\n").splitlines())
    except ValueError as e:
        assert "Parse/canada.json" in str(e), e
    else:
        raise AssertionError("an unpaired row passed")
    print("ok")


if __name__ == "__main__":
    if sys.argv[1:] == ["--check"]:
        check()
    else:
        try:
            sys.stdout.write(table(sys.stdin))
        except ValueError as e:
            sys.exit(f"table.py: {e}")
