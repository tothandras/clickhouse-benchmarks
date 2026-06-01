#!/usr/bin/env python3
"""Value-parity check across scenarios.

Runs each PAIRED query against baseline_openmeter_events and proposal_events with
IDENTICAL parameters and asserts the result sets are VALUE-equal — proving the
same seeded events produce the same meter outputs regardless of table design.
Also checks the proposal rollup-served queries against their base-table value
oracle (the rollup result must equal the direct base-table computation).

Two real, expected representation differences are normalized so they don't read
as false mismatches:
  * baseline stores `data` as String, proposal as JSON → numeric text can differ
    in trailing digits (e.g. 12.30 vs 12.3). We round every numeric cell to 9 dp.
  * the simple rollup grouped query (kong_status_by_route_rollup) keys on whole
    hours, so it is only billing-exact for HOUR-ALIGNED windows (non-aligned
    from/to need the 3-part hybrid). We therefore derive an hour-aligned [from,to).

Params mirror the Go harness defaultParams (subject-%05d, ns=default, …).
Exit 0 if all PASS (or SKIP); exit 1 on any MISMATCH/ERROR.
"""
import subprocess, sys, re, os
from decimal import Decimal, InvalidOperation

CH = ["clickhouse", "client", "--host", "127.0.0.1", "--port", "9000"]
QDIR = "scenarios/{}/queries"

def ch(sql):
    r = subprocess.run(CH + ["-q", sql], capture_output=True, text=True)
    if r.returncode != 0:
        msg = r.stderr.strip().splitlines()
        raise RuntimeError(msg[-1] if msg else "CH error")
    return r.stdout

def derive_params():
    subjects = "(" + ", ".join(f"'subject-{i:05d}'" for i in range(10)) + ")"
    # HOUR-ALIGNED window covering all data, so the simple rollup grouped query is exact.
    lo = ch("SELECT toString(toStartOfHour(min(time))) FROM proposal_events").strip()
    hi = ch("SELECT toString(toStartOfHour(max(time)) + INTERVAL 1 HOUR) FROM proposal_events").strip()
    g1 = ch("SELECT toString(data.group1) FROM proposal_events WHERE type='api_request' "
            "AND data.group1 IS NOT NULL ORDER BY 1 LIMIT 1").strip()
    g2 = ch("SELECT toString(data.group2) FROM proposal_events WHERE type='api_request' "
            "AND data.group2 IS NOT NULL ORDER BY 1 LIMIT 1").strip()
    return {
        "namespace": "'default'", "type": "'api_request'", "subjects": subjects,
        "from": f"'{lo}'", "to": f"'{hi}'",
        "group1": f"'{g1}'", "group2": f"'{g2}'", "model": "'claude-haiku'",
    }

def render(sql, params):
    def sub(m):
        v = params.get(m.group(1))
        if v is None:
            raise KeyError(f"unbound param {m.group(1)}")
        return v
    return re.sub(r"\{(\w+):[^}]+\}", sub, sql)

def normalize(tsv):
    """Make two result sets value-comparable across the String vs JSON `data`
    layouts. Float aggregates (avg/min/max/latest) accumulate in a different
    summation order per layout, so the low-order bits differ — round numeric
    cells to 6 significant decimals. Row order can also differ when a non-unique
    GROUP BY key ties, so we sort the data rows (keeping the header first)."""
    lines = tsv.rstrip("\n").split("\n")
    if not lines:
        return ""
    header, rows = lines[0], lines[1:]
    norm = []
    for line in rows:
        cells = []
        for c in line.split("\t"):
            try:
                cells.append(format(round(Decimal(c), 6), "f"))
            except (InvalidOperation, ValueError):
                cells.append(c)
        norm.append("\t".join(cells))
    norm.sort()
    return header + "\n" + "\n".join(norm)

def run_query(scenario, fname, params):
    sql = open(f"{QDIR.format(scenario)}/{fname}").read().rstrip().rstrip(";")
    return normalize(ch(render(sql, params) + "\nFORMAT TSVWithNames"))

def main():
    params = derive_params()
    bdir, pdir = QDIR.format("baseline-openmeter"), QDIR.format("proposal")
    common = sorted(f for f in (set(os.listdir(bdir)) & set(os.listdir(pdir))) if f.endswith(".sql"))

    # lookup_by_id returns the whole `data` column; JSON canonicalizes key order /
    # number form vs String's raw bytes, so the row is not value-comparable. Perf-only.
    SKIP_VALUE = {"lookup_by_id.sql"}

    results = []
    for f in common:
        if f in SKIP_VALUE:
            results.append((f, "SKIP", "returns raw `data`; JSON vs String repr differs by design")); continue
        try:
            b = run_query("baseline-openmeter", f, params)
            p = run_query("proposal", f, params)
            results.append((f, "PASS" if b == p else "MISMATCH",
                            "" if b == p else f"baseline {len(b)}B vs proposal {len(p)}B"))
        except Exception as e:
            results.append((f, "ERROR", str(e)[:140]))

    ORACLE = {
        "kong_api_request_total_hybrid.sql": "kong_api_request_total.sql",
        "kong_llm_tokens_total_hybrid.sql": "kong_llm_tokens_total.sql",
        "kong_status_by_route_rollup.sql": "kong_status_by_route.sql",
    }
    for roll, oracle in ORACLE.items():
        try:
            r = run_query("proposal", roll, params)
            o = run_query("proposal", oracle, params)
            results.append((f"{roll} vs {oracle}", "PASS" if r == o else "MISMATCH",
                            "" if r == o else f"rollup {len(r)}B vs oracle {len(o)}B"))
        except Exception as e:
            results.append((f"{roll} vs {oracle}", "ERROR", str(e)[:140]))

    w = max(len(r[0]) for r in results)
    n = {"PASS": 0, "MISMATCH": 0, "ERROR": 0, "SKIP": 0}
    print(f"\nparams: from={params['from']} to={params['to']} (hour-aligned) subjects=subject-00000..09\n")
    for name, status, note in results:
        print(f"  {name:<{w}}  {status:<9} {note}")
        n[status] += 1
    print(f"\n  PASS={n['PASS']} MISMATCH={n['MISMATCH']} ERROR={n['ERROR']} SKIP={n['SKIP']}")
    sys.exit(1 if (n["MISMATCH"] or n["ERROR"]) else 0)

if __name__ == "__main__":
    main()
