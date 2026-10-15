// SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"fmt"
	"html"
	"sort"
	"strings"
)

// The dashboard is deliberately one self-contained file: inline SVG, no CDN and
// no JavaScript dependency. It renders offline and adds nothing to the
// repository's dependency surface.

const dashboardCSS = `
:root{--bg:#0f1115;--panel:#171a21;--line:#252a34;--fg:#e6e9ef;--dim:#98a1b3;--nv:#76b900;--warn:#e8b339;--bad:#e05252}
*{box-sizing:border-box}
body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.55 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif}
.wrap{max-width:960px;margin:0 auto;padding:34px 22px 70px}
h1{font-size:23px;margin:0 0 4px}
h2{font-size:15px;margin:0 0 14px;letter-spacing:.03em;text-transform:uppercase;color:var(--dim);font-weight:600}
.sub{color:var(--dim);margin:0 0 26px;font-size:13px}
.card{background:var(--panel);border:1px solid var(--line);border-radius:9px;padding:20px 22px;margin-bottom:18px}
.cause{border-left:3px solid var(--nv);padding:11px 0 11px 15px;margin-bottom:15px}
.cause:last-child{margin-bottom:0}
.cause .t{font-weight:600;margin-bottom:3px}
.cause .d{color:var(--dim)}
.cause .f{color:var(--nv);margin-top:5px;font-size:13px}
.cause.note{border-left-color:var(--warn)}
.cause.note .f{color:var(--warn)}
.stats{display:flex;gap:34px;flex-wrap:wrap;margin-bottom:6px}
.stat .v{font-size:26px;font-weight:600}
.stat .k{color:var(--dim);font-size:12px;text-transform:uppercase;letter-spacing:.05em}
.chart{width:100%;height:auto}
.grid{stroke:var(--line);stroke-width:1}
.l50{fill:none;stroke:var(--nv);stroke-width:2.2}
.l90{fill:none;stroke:#3d6ea8;stroke-width:1.6;stroke-dasharray:5 4}
.dot{fill:var(--nv)}
.ylab{fill:var(--dim);font-size:11px;text-anchor:end}
.xlab{fill:var(--dim);font-size:10px;text-anchor:middle}
.xcnt{fill:#5d6675;font-size:9px;text-anchor:middle}
.rowlab{fill:var(--fg);font-size:12px;text-anchor:end}
.rowval{fill:var(--dim);font-size:11px}
.bq{fill:var(--warn)}
.be{fill:var(--nv)}
.legend{color:var(--dim);font-size:12px;margin-top:10px}
.legend i{display:inline-block;width:10px;height:10px;border-radius:2px;margin:0 5px 0 16px;vertical-align:middle}
.legend i:first-child{margin-left:0}
.quota{position:relative;background:#22262f;border-radius:5px;height:32px;overflow:hidden}
.quotafill{height:100%}
.quotafill.ok{background:var(--nv)}.quotafill.warn{background:var(--warn)}.quotafill.over{background:var(--bad)}
.quotatxt{position:absolute;left:12px;top:7px;font-size:13px;font-weight:600;text-shadow:0 1px 3px rgba(0,0,0,.75)}
table{width:100%;border-collapse:collapse;font-size:13px}
th{text-align:left;color:var(--dim);font-weight:600;padding:7px 8px;border-bottom:1px solid var(--line)}
td{padding:7px 8px;border-bottom:1px solid var(--line)}
td.n{text-align:right;font-variant-numeric:tabular-nums}
.empty{color:var(--dim)}
code{background:#22262f;padding:1px 5px;border-radius:3px;font-size:12px}
`

func esc(s string) string { return html.EscapeString(s) }

func lineChart(weeks []Week) string {
	if len(weeks) == 0 {
		return "<p class='empty'>no data</p>"
	}
	const w, h, pad = 860.0, 272.0, 46.0
	top := 1.0
	for _, k := range weeks {
		if k.P90 > top {
			top = k.P90
		}
	}
	top *= 1.15
	n := len(weeks)
	step := (w - pad*2) / math1(n-1)
	x := func(i int) float64 { return pad + float64(i)*step }
	y := func(v float64) float64 { return h - pad - (v/top)*(h-pad*2) }

	var b strings.Builder
	fmt.Fprintf(&b, "<svg viewBox='0 0 %.0f %.0f' class='chart'>", w, h)
	for g := 0; g < 5; g++ {
		gy := pad + float64(g)*(h-pad*2)/4
		fmt.Fprintf(&b, "<line x1='%.0f' y1='%.1f' x2='%.0f' y2='%.1f' class='grid'/>", pad, gy, w-pad, gy)
		fmt.Fprintf(&b, "<text x='%.0f' y='%.1f' class='ylab'>%.0fm</text>", pad-8, gy+4, top*(1-float64(g)/4))
	}
	path := func(pick func(Week) float64) string {
		var p strings.Builder
		for i, k := range weeks {
			cmd := "L"
			if i == 0 {
				cmd = "M"
			}
			fmt.Fprintf(&p, "%s%.1f,%.1f ", cmd, x(i), y(pick(k)))
		}
		return strings.TrimSpace(p.String())
	}
	fmt.Fprintf(&b, "<path d='%s' class='l90'/>", path(func(k Week) float64 { return k.P90 }))
	fmt.Fprintf(&b, "<path d='%s' class='l50'/>", path(func(k Week) float64 { return k.P50 }))
	for i, k := range weeks {
		fmt.Fprintf(&b, "<circle cx='%.1f' cy='%.1f' r='3.5' class='dot'><title>%s: median %.1f min, p90 %.1f min, %d runs</title></circle>",
			x(i), y(k.P50), esc(k.Label), k.P50, k.P90, k.Count)
		label := k.Label
		if len(label) > 3 {
			label = label[len(label)-3:]
		}
		fmt.Fprintf(&b, "<text x='%.1f' y='%.0f' class='xlab'>%s</text>", x(i), h-pad+18, esc(label))
		// The run count sits under the label so a thin week is never mistaken
		// for a real speedup.
		fmt.Fprintf(&b, "<text x='%.1f' y='%.0f' class='xcnt'>n=%d</text>", x(i), h-pad+31, k.Count)
	}
	b.WriteString("</svg>")
	return b.String()
}

func math1(n int) float64 {
	if n < 1 {
		return 1
	}
	return float64(n)
}

type barRow struct {
	Name    string
	Queue   float64
	Exec    float64
	Runs    int
	Skipped int
}

func stackedBars(rows []barRow) string {
	if len(rows) == 0 {
		return "<p class='empty'>no data</p>"
	}
	const w, bar, pad = 860.0, 26.0, 210.0
	top := 0.0
	for _, r := range rows {
		if r.Queue+r.Exec > top {
			top = r.Queue + r.Exec
		}
	}
	if top == 0 {
		top = 1
	}
	span := w - pad - 90
	h := float64(len(rows))*(bar+8) + 16

	var b strings.Builder
	fmt.Fprintf(&b, "<svg viewBox='0 0 %.0f %.0f' class='chart'>", w, h)
	for i, r := range rows {
		y := 8 + float64(i)*(bar+8)
		qw := r.Queue / top * span
		ew := r.Exec / top * span
		short := r.Name
		if len([]rune(short)) > 30 {
			short = string([]rune(short)[:29]) + "…"
		}
		fmt.Fprintf(&b, "<text x='%.0f' y='%.0f' class='rowlab'>%s</text>", pad-10, y+bar*0.68, esc(short))
		fmt.Fprintf(&b, "<rect x='%.0f' y='%.0f' width='%.1f' height='%.0f' class='bq'><title>%s: queue %.1f min</title></rect>",
			pad, y, qw, bar, esc(r.Name), r.Queue)
		fmt.Fprintf(&b, "<rect x='%.1f' y='%.0f' width='%.1f' height='%.0f' class='be'><title>%s: execute %.1f min over %d runs, %d skipped</title></rect>",
			pad+qw, y, ew, bar, esc(r.Name), r.Exec, r.Runs, r.Skipped)
		fmt.Fprintf(&b, "<text x='%.1f' y='%.0f' class='rowval'>%.1fm</text>", pad+qw+ew+8, y+bar*0.68, r.Queue+r.Exec)
	}
	b.WriteString("</svg>")
	return b.String()
}

func quotaBar(c CacheState) string {
	used := c.TotalGB / quotaGB * 100
	if used > 100 {
		used = 100
	}
	cls := "ok"
	if used >= 90 {
		cls = "over"
	} else if used >= 75 {
		cls = "warn"
	}
	return fmt.Sprintf("<div class='quota'><div class='quotafill %s' style='width:%.1f%%'></div>"+
		"<span class='quotatxt'>%.2f GB / %.0f GB (%.0f%%) across %d entries</span></div>",
		cls, used, c.TotalGB, quotaGB, used, c.Count)
}

func buildRows(stats map[string]*JobStat) []barRow {
	var rows []barRow
	for n, s := range stats {
		if s.Runs == 0 || isGate(n) {
			continue
		}
		rows = append(rows, barRow{Name: n, Queue: pctl(s.Queue, 0.5), Exec: pctl(s.Exec, 0.5), Runs: s.Runs, Skipped: s.Skipped})
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i].Queue+rows[i].Exec, rows[j].Queue+rows[j].Exec
		if a == b {
			return rows[i].Name < rows[j].Name
		}
		return a > b
	})
	if len(rows) > 16 {
		rows = rows[:16]
	}
	return rows
}

func renderHTML(repo, workflow string, runs []Run, stats map[string]*JobStat,
	poles map[string]int, poleRuns int, cache CacheState, causes []Cause,
	wall float64, generated string, truncated bool) string {

	var causeHTML strings.Builder
	for _, c := range causes {
		cls := "cause"
		if c.Note {
			cls += " note"
		}
		fmt.Fprintf(&causeHTML, "<div class='%s'><div class='t'>%s</div><div class='d'>%s</div><div class='f'>%s</div></div>",
			cls, esc(c.Title), esc(c.Detail), esc(c.Fix))
	}
	if len(causes) == 0 {
		causeHTML.WriteString("<p class='empty'>Nothing is dominating the wall clock right now.</p>")
	}

	type famRow struct {
		name string
		size float64
	}
	var fams []famRow
	for n, s := range cache.Families {
		fams = append(fams, famRow{n, s})
	}
	sort.Slice(fams, func(i, j int) bool {
		if fams[i].size == fams[j].size {
			return fams[i].name < fams[j].name
		}
		return fams[i].size > fams[j].size
	})
	var famHTML strings.Builder
	for i, f := range fams {
		if i >= 8 {
			break
		}
		fmt.Fprintf(&famHTML, "<tr><td>%s</td><td class='n'>%.2f GB</td><td class='n'>%.0f%%</td><td class='n'>%d</td></tr>",
			esc(f.name), f.size, f.size/quotaGB*100, cache.Copies[f.name])
	}

	type poleRow struct {
		name string
		n    int
	}
	var pl []poleRow
	for n, c := range poles {
		pl = append(pl, poleRow{n, c})
	}
	sort.Slice(pl, func(i, j int) bool {
		if pl[i].n == pl[j].n {
			return pl[i].name < pl[j].name
		}
		return pl[i].n > pl[j].n
	})
	var poleHTML strings.Builder
	if poleRuns > 0 {
		poleHTML.WriteString("<div class='card'><h2>What finishes last</h2><table><tr><th>Job</th><th class='n'>Runs where it was last</th><th class='n'>Share</th></tr>")
		for i, p := range pl {
			if i >= 8 {
				break
			}
			fmt.Fprintf(&poleHTML, "<tr><td>%s</td><td class='n'>%d</td><td class='n'>%.0f%%</td></tr>",
				esc(p.name), p.n, float64(p.n)/float64(poleRuns)*100)
		}
		poleHTML.WriteString("</table></div>")
	}

	mins := make([]float64, 0, len(runs))
	for _, r := range runs {
		mins = append(mins, r.Minutes)
	}
	weeks := weekly(runs, truncated)
	span := "no runs"
	if len(runs) > 0 {
		span = fmt.Sprintf("%s to %s", runs[len(runs)-1].Start.Format("2006-01-02"), runs[0].Start.Format("2006-01-02"))
	}

	stranded := ""
	if cache.StrandedGB > 0 {
		stranded = fmt.Sprintf("<p class='sub' style='margin:12px 0 0'>%.2f GB is stored on <code>gh-readonly-queue</code> refs. "+
			"Those branches are deleted when the merge queue drains, so the entries can never be restored but still count against quota.</p>", cache.StrandedGB)
	}

	return fmt.Sprintf(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>Build health: %s</title><style>%s</style></head><body><div class="wrap">
<h1>Why is my build slow?</h1>
<p class="sub">%s &middot; workflow <code>%s</code> &middot; %d successful runs (%s) &middot;
per-job detail from the most recent %d runs &middot; generated %s</p>

<div class="card"><h2>Ranked causes</h2>%s</div>

<div class="card"><h2>Wall clock</h2>
<div class="stats">
  <div class="stat"><div class="v">%.1f min</div><div class="k">median run</div></div>
  <div class="stat"><div class="v">%.1f min</div><div class="k">p90 run</div></div>
  <div class="stat"><div class="v">%d</div><div class="k">weeks of history</div></div>
</div>
%s
<div class="legend"><i style="background:var(--nv)"></i>median<i style="background:#3d6ea8"></i>p90
&middot; hover a point for the week and run count</div></div>

<div class="card"><h2>Where each job's time goes</h2>
%s
<div class="legend"><i style="background:var(--warn)"></i>waiting for a runner
<i style="background:var(--nv)"></i>executing &middot; median per job; gate jobs excluded</div></div>

%s

<div class="card"><h2>Cache</h2>
%s
<p class="sub" style="margin:14px 0 10px">Largest families. GitHub allows %.0f GB per repository and
evicts least-recently-used entries once full.</p>
<table><tr><th>Family</th><th class="n">Size</th><th class="n">Quota</th><th class="n">Copies</th></tr>%s</table>
%s</div>
</div></body></html>`,
		esc(repo), dashboardCSS,
		esc(repo), esc(workflow), len(runs), esc(span), poleRuns, esc(generated),
		causeHTML.String(),
		wall, pctl(mins, 0.9), len(weeks),
		lineChart(weeks),
		stackedBars(buildRows(stats)),
		poleHTML.String(),
		quotaBar(cache), quotaGB, famHTML.String(), stranded)
}
