import { useEffect, useState } from "react";
import { api } from "../api.js";
import { Panel, Table, report } from "../components/ui.jsx";

const periods = [
  ["all", "全部"],
  ["today", "今天"],
  ["7d", "近 7 天"],
  ["30d", "近 30 天"],
];

function duration(record) {
  if (typeof record.ms === "number") return record.ms + " ms";
  const started = Date.parse(record.started);
  const finished = Date.parse(record.finished);
  if (Number.isNaN(started) || Number.isNaN(finished)) return "";
  return Math.max(0, finished - started) + " ms";
}

function countOf(usage, keys) {
  for (const key of keys) if (typeof usage[key] === "number") return usage[key];
  return null;
}

function hasCounts(record) {
  return ["in", "out", "cacheRead", "cacheWrite", "reasoning"].some((key) => typeof record[key] === "number");
}

function usageLabel(record) {
  if (hasCounts(record)) {
    const parts = ["输入 " + record.in, "输出 " + record.out];
    if (record.cacheRead) parts.push("缓存读 " + record.cacheRead);
    if (record.cacheWrite) parts.push("缓存写 " + record.cacheWrite);
    if (record.reasoning) parts.push("推理 " + record.reasoning);
    return parts.join(" · ");
  }
  return rawUsageLabel(record.usage);
}

function rawUsageLabel(usage) {
  if (!usage || typeof usage !== "object") return "未知";
  const input = countOf(usage, ["prompt_tokens", "input_tokens", "promptTokenCount"]);
  const output = countOf(usage, ["completion_tokens", "output_tokens", "candidatesTokenCount"]);
  const total = countOf(usage, ["total_tokens", "totalTokenCount"]);
  const details = usage.prompt_tokens_details;
  const cached = details && typeof details.cached_tokens === "number" ? details.cached_tokens : typeof usage.cache_read_input_tokens === "number" ? usage.cache_read_input_tokens : null;
  const parts = [];
  if (input != null) parts.push("输入 " + input);
  if (output != null) parts.push("输出 " + output);
  if (total != null) parts.push("合计 " + total);
  if (cached != null) parts.push("缓存 " + cached);
  return parts.length ? parts.join(" · ") : JSON.stringify(usage);
}

function outcomeLabel(record) {
  if (record.status) return record.outcome + " · " + record.status;
  return record.outcome || "";
}

function balanceStatus(row) {
  if (row.status === "unsupported") return "该厂商没有余额接口";
  if (row.status !== "ok") return "查询失败";
  if (row.available === false) return "余额不足";
  return "可调用";
}

function Balances({ rows }) {
  return <Panel title="厂商密钥余额" description="剩余是向该厂商的余额接口现查的。DeepSeek 用 GET /user/balance。没有余额接口的厂商不会被请求。"><Table headers={["提供方", "密钥", "剩余", "状态"]} rows={(rows || []).map((row) => [
    row.name || row.providerId,
    "密钥 " + row.index,
    row.status === "ok" ? row.remaining : "—",
    balanceStatus(row),
  ])} empty="还没有厂商密钥"/></Panel>;
}

function quotaUsed(win) {
  if (typeof win.usedPercent === "number") return win.usedPercent + "% 已用";
  if (typeof win.remainingFraction === "number") return Math.round(win.remainingFraction * 100) + "% 剩余";
  return "—";
}

function quotaReset(win) {
  if (win.reset) {
    const at = new Date(win.reset);
    if (!Number.isNaN(at.getTime())) return at.toLocaleString();
  }
  return win.resetText || "—";
}

function Quotas({ rows }) {
  const lines = [];
  for (const quota of rows || []) {
    for (const win of quota.windows || []) {
      lines.push([quota.name, win.name, quotaUsed(win), quotaReset(win)]);
    }
    if (quota.credits) lines.push([quota.name, "另购 credits", quota.credits, "—"]);
    if (quota.status !== "ok") lines.push([quota.name, "—", "—", "查询失败"]);
  }
  return <Panel title="订阅额度" description="剩余是现查的：Codex 问 ChatGPT 后端的 /wham/usage，Claude 让本机的 claude 自己回答 /usage，Gemini 和 Antigravity 问 Code Assist 的配额接口。查不到显示查询失败，不当作 0。"><Table headers={["账号", "窗口", "用量", "重置"]} rows={lines} empty="没有已登录的订阅账号"/></Panel>;
}

function Stats({ summary }) {
  if (!summary) return null;
  const items = [
    ["调用", summary.calls],
    ["输入", summary.in],
    ["输出", summary.out],
    ["缓存读", summary.cacheRead],
    ["缓存写", summary.cacheWrite],
    ["推理", summary.reasoning],
    ["费用", typeof summary.cost === "number" ? "$" + summary.cost.toFixed(6) : "—"],
    ["无标价", summary.unpriced ?? 0],
  ];
  return <div className="usage-stats">{items.map(([label, value]) => <div className="usage-stat" key={label}><span>{label}</span><strong>{value ?? 0}</strong></div>)}</div>;
}

export function Usage({ tick, setMessage }) {
  const [period, setPeriod] = useState("all");
  const [routeId, setRouteId] = useState("");
  const [providerId, setProviderId] = useState("");
  const [start, setStart] = useState("");
  const [end, setEnd] = useState("");
  const [body, setBody] = useState(null);

  async function load(event) {
    if (event) event.preventDefault();
    const query = new URLSearchParams();
    if (period) query.set("period", period);
    if (routeId.trim()) query.set("routeId", routeId.trim());
    if (providerId.trim()) query.set("providerId", providerId.trim());
    if (start.trim()) query.set("start", start.trim());
    if (end.trim()) query.set("end", end.trim());
    try { setBody(await api("GET", "/usage" + (query.toString() ? "?" + query : ""))); }
    catch (err) { report(setMessage, err); }
  }

  useEffect(() => { load(); }, [tick]);
  const records = body && body.status !== "unknown" ? (body.records || []) : [];
  return <div className="stack">
    <Panel title="筛选调用记录" description="窗口按这台机器的本地日历日。开始和结束仍用 RFC3339，比窗口起点更晚的开始时间会再收窄。">
      <form className="grid filters" onSubmit={load}><label>窗口<select value={period} onChange={(e) => setPeriod(e.target.value)}>{periods.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label><label>路由 ID<input value={routeId} onChange={(e) => setRouteId(e.target.value)}/></label><label>提供方 ID<input value={providerId} onChange={(e) => setProviderId(e.target.value)}/></label><label>开始时间<input value={start} placeholder="RFC3339" onChange={(e) => setStart(e.target.value)}/></label><label>结束时间<input value={end} placeholder="RFC3339" onChange={(e) => setEnd(e.target.value)}/></label><div className="form-actions"><button type="submit">应用筛选</button></div></form>
    </Panel>
    {body && body.status === "unknown" ? <p className="note standalone">用量存储不可读，不能当成 0。</p> : <><Stats summary={body && body.summary}/><Balances rows={body && body.balances}/><Quotas rows={body && body.quotas}/><Panel title="调用记录" description={`当前结果 ${records.length} 条。一次尝试一行。`} className="table-panel"><Table headers={["开始", "模型", "调用方", "会话", "路由", "提供方", "结果", "耗时", "用量"]} rows={records.map((record) => [record.started, <strong className="cell-title">{record.model || "—"}</strong>, record.caller || record.tokenId || "—", record.session || "—", record.routeId, record.providerId, outcomeLabel(record), duration(record), usageLabel(record)])} empty="没有符合筛选条件的调用记录"/></Panel></>}
  </div>;
}
