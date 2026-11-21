import { useEffect, useState } from "react";
import { api } from "../api.js";
import { Panel, Table, report } from "../components/ui.jsx";

function duration(record) {
  const started = Date.parse(record.started);
  const finished = Date.parse(record.finished);
  if (Number.isNaN(started) || Number.isNaN(finished)) return "";
  return Math.max(0, finished - started) + " ms";
}

function countOf(usage, keys) {
  for (const key of keys) if (typeof usage[key] === "number") return usage[key];
  return null;
}

function usageLabel(usage) {
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

export function Usage({ tick, setMessage }) {
  const [routeId, setRouteId] = useState("");
  const [providerId, setProviderId] = useState("");
  const [start, setStart] = useState("");
  const [end, setEnd] = useState("");
  const [body, setBody] = useState(null);

  async function load(event) {
    if (event) event.preventDefault();
    const query = new URLSearchParams();
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
    <Panel title="筛选调用记录" description="时间使用 RFC3339 格式，例如 2026-01-01T00:00:00Z">
      <form className="grid filters" onSubmit={load}><label>路由 ID<input value={routeId} onChange={(e) => setRouteId(e.target.value)}/></label><label>提供方 ID<input value={providerId} onChange={(e) => setProviderId(e.target.value)}/></label><label>开始时间<input value={start} placeholder="RFC3339" onChange={(e) => setStart(e.target.value)}/></label><label>结束时间<input value={end} placeholder="RFC3339" onChange={(e) => setEnd(e.target.value)}/></label><div className="form-actions"><button type="submit">应用筛选</button></div></form>
    </Panel>
    {body && body.status === "unknown" ? <p className="note standalone">用量存储不可读，不能当成 0。</p> : <Panel title="调用记录" description={`当前结果 ${records.length} 条`} className="table-panel"><Table headers={["开始", "模型", "调用方", "会话", "路由", "提供方", "结果", "耗时", "用量"]} rows={records.map((record) => [record.started, <strong className="cell-title">{record.model || "—"}</strong>, record.caller || record.tokenId || "—", record.session || "—", record.routeId, record.providerId, record.outcome, duration(record), usageLabel(record.usage)])} empty="没有符合筛选条件的调用记录"/></Panel>}
  </div>;
}
