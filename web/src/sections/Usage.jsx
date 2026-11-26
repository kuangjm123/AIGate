import { useEffect, useState } from "react";
import { api, download } from "../api.js";
import { Panel, Table, report } from "../components/ui.jsx";

const periods = [
  ["all", "全部"],
  ["today", "今天"],
  ["7d", "近 7 天"],
  ["30d", "近 30 天"],
];

const pageSize = 100;

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
    const parts = ["输入 " + (record.in || 0), "输出 " + (record.out || 0)];
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
  const mark = record.handedOn ? "已移交" : record.status ? record.outcome + " · " + record.status : record.outcome || "";
  return record.tries > 1 ? mark + " · 重试 " + record.tries : mark;
}

function sourceLabel(record) {
  switch (record.source) {
    case "reported": return <span className="pill">补报</span>;
    case "probe": return <span className="pill">探针</span>;
    case "mcp": return <span className="pill">MCP</span>;
    case "search": return <span className="pill">搜索</span>;
    default: return record.handedOn ? <span className="pill">已计费失利</span> : "—";
  }
}

function balanceStatus(row) {
  if (row.status === "manual") return "手填";
  if (row.status === "unsupported") return "该厂商没有余额接口";
  if (row.stale) return "刷新失败，显示上次读数" + (row.error ? "：" + row.error : "");
  if (row.status !== "ok") return row.error ? "查询失败：" + row.error : "查询失败";
  if (row.available === false) return "余额不足";
  return "可调用";
}

function Balances({ rows }) {
  return <Panel title="厂商密钥余额" description="剩余是向该厂商的余额接口现查的。没有余额接口的主机不会被请求；手填的余额按填写的值显示。"><Table headers={["提供方", "密钥", "剩余", "状态"]} rows={(rows || []).map((row) => [
    row.name || row.providerId,
    "密钥 " + row.index,
    row.remaining != null && row.remaining !== "" ? row.remaining : "—",
    <span title={row.error || undefined}>{balanceStatus(row)}</span>,
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
  return <Panel title="订阅额度" description="剩余是现查的：Codex 问 ChatGPT 后端，Claude 让本机的 claude 回答 /usage，Gemini 和 Antigravity 问 Code Assist。查不到显示查询失败，不当作 0。"><Table headers={["账号", "窗口", "用量", "重置"]} rows={lines} empty="没有已登录的订阅账号"/></Panel>;
}

function Stats({ summary }) {
  if (!summary) return null;
  const items = [
    ["调用", summary.calls],
    ["失败", summary.failed ?? 0],
    ["取消", summary.canceled ?? 0],
    ["重试", summary.retried ?? 0],
    ["输入", summary.in],
    ["输出", summary.out],
    ["缓存读", summary.cacheRead],
    ["缓存写", summary.cacheWrite],
    ["其中 1h 缓存写", summary.cacheWrite1h ?? 0],
    ["推理", summary.reasoning],
    ["费用", typeof summary.cost === "number" ? "$" + summary.cost.toFixed(6) : "—"],
    ["无标价", summary.unpriced ?? 0],
  ];
  return <div className="usage-stats">{items.map(([label, value]) => <div className="usage-stat" key={label}><span>{label}</span><strong>{value ?? 0}</strong></div>)}</div>;
}

function Groups({ summary }) {
  if (!summary) return null;
  const groups = [
    ["Agent", summary.agents],
    ["模型", summary.models],
    ["上游密钥", summary.providerKeys],
    ["订阅账号", summary.accounts],
    ["调用方密钥", summary.callerKeys],
    ["会话", summary.sessions],
  ];
  return <div className="usage-groups">{groups.map(([title, rows]) => <Panel key={title} title={title} description={`${(rows || []).length} 组`} className="table-panel"><Table headers={["键", "调用", "输入", "输出", "缓存读", "缓存写", "推理", "费用", "无标价"]} rows={(rows || []).map((row) => [
    <code>{row.key}</code>, row.calls, row.in, row.out, row.cacheRead || 0, row.cacheWrite || 0, row.reasoning || 0, "$" + (row.cost || 0).toFixed(6), row.unpriced || 0,
  ])} empty="没有分组"/></Panel>)}</div>;
}

function pointLabel(point, period) {
  const at = new Date(point.start);
  if (Number.isNaN(at.getTime())) return point.start;
  if (period === "today") return String(at.getHours()).padStart(2, "0") + ":00";
  return (at.getMonth() + 1) + "/" + at.getDate();
}

function Series({ summary, period }) {
  const points = (summary && summary.series) || [];
  if (!points.length) return null;
  const max = Math.max(1, ...points.map((point) => point.calls || 0));
  return <Panel title="时间序列" description={period === "today" ? "按小时" : period === "all" ? "按周" : "按天"}>
    <div className="series-bars">{points.map((point) => <div className="series-row" key={point.start}>
      <span>{pointLabel(point, period)}</span>
      <div className="series-track"><div className="series-fill" style={{ width: Math.max(0, Math.min(100, ((point.calls || 0) / max) * 100)) + "%" }}/></div>
      <strong>{point.calls || 0}</strong>
      <small>${(point.cost || 0).toFixed(4)}</small>
    </div>)}</div>
  </Panel>;
}

function heatLevel(calls, max) {
  if (!calls) return 0;
  if (max <= 1) return 4;
  const ratio = calls / max;
  if (ratio > 0.75) return 4;
  if (ratio > 0.5) return 3;
  if (ratio > 0.25) return 2;
  return 1;
}

function Heatmap({ days }) {
  if (!days || !days.length) return null;
  const max = Math.max(1, ...days.map((day) => day.calls || 0));
  const first = new Date(days[0].date + "T00:00:00");
  const lead = Number.isNaN(first.getTime()) ? 0 : (first.getDay() + 6) % 7;
  return <Panel title="调用热力图" description="最近 53 周，每天一格。颜色越深，调用越多；使用当前筛选条件。">
    <div className="heatmap" role="img" aria-label="最近 53 周的每日调用次数">
      {Array.from({ length: lead }, (_, index) => <span className="heat-cell heat-0 blank" key={"blank-" + index}/>)}
      {days.map((day) => <span className={"heat-cell heat-" + heatLevel(day.calls, max)} key={day.date} title={`${day.date} · ${day.calls || 0} 次调用 · ${day.failed || 0} 失败 · ${day.in || 0} 输入 · ${day.out || 0} 输出 · $${(day.cost || 0).toFixed(6)}`}/>)}
    </div>
    <div className="heat-legend"><span>少</span>{[0, 1, 2, 3, 4].map((level) => <span key={level} className={"heat-cell heat-" + level}/>)}<span>多</span></div>
  </Panel>;
}

function Traffic({ snapshot }) {
  if (!snapshot) return null;
  const live = snapshot.live === true;
  return <Panel title="当前流量" description={live ? "网关内存里的实时计数，不读历史用量文件。" : "网关还没有提供实时计数；这里保持为零，并且不读历史用量文件。"}>
    <div className="usage-stats">
      <div className="usage-stat"><span>进行中请求</span><strong>{snapshot.activeRequests ?? 0}</strong></div>
      <div className="usage-stat"><span>上行字节</span><strong>{snapshot.bytesIn ?? 0}</strong></div>
      <div className="usage-stat"><span>下行字节</span><strong>{snapshot.bytesOut ?? 0}</strong></div>
      <div className="usage-stat"><span>取样时间</span><strong className="mono-small">{snapshot.at || "—"}</strong></div>
    </div>
  </Panel>;
}

function ConversationDetail({ detail, onClose }) {
  if (!detail) return null;
  const body = detail.body;
  const rows = body && body.status !== "unknown" ? (body.turns || []) : [];
  return <Panel title="打开调用原文" description={detail.session + (detail.agent ? " · " + detail.agent : "")} className="table-panel" actions={<button type="button" className="ghost" onClick={onClose}>关闭</button>}>
    {!body ? <p className="form-note">正在读取会话存档…</p> : null}
    {body && body.status === "unknown" ? <p className="note standalone">这段会话存档不可读，不能当成没有内容。</p> : null}
    {body && body.status === "ok" && !rows.length ? <p className="form-note standalone">这段时间里没有可读的会话正文。用量行本身从不保存提示词或回复。</p> : null}
    {rows.length ? <section className="conversation-detail">{rows.map((turn, index) => <article key={(turn.at || "") + index} className="turn"><header><span className="turn-index">{String(index + 1).padStart(2, "0")}</span><span>{turn.at}</span><span>{turn.model || "未知模型"}</span><span>{turn.caller || ""}</span></header><div className="turn-body">{turn.input ? <div className="message-block input"><strong>输入</strong><p className="turn-text">{turn.input}</p></div> : null}<div className="message-block output"><strong>输出</strong><p className="turn-text">{turn.output}</p></div></div></article>)}</section> : null}
  </Panel>;
}

function Pricing({ setMessage }) {
  const [prices, setPrices] = useState("[]");
  const [peaks, setPeaks] = useState("[]");
  const [open, setOpen] = useState(false);

  async function load() {
    try {
      const body = await api("GET", "/pricing");
      setPrices(JSON.stringify(body.prices || [], null, 2));
      setPeaks(JSON.stringify(body.peaks || [], null, 2));
    } catch (err) { report(setMessage, err); }
  }

  useEffect(() => { if (open) load(); }, [open]);

  async function save(event) {
    event.preventDefault();
    try {
      await api("PUT", "/pricing", { prices: JSON.parse(prices), peaks: JSON.parse(peaks) });
      setMessage({ kind: "note", text: "价目与高峰窗口已保存。历史费用按新价重算，不回写用量文件。" });
    } catch (err) {
      if (err instanceof SyntaxError) setMessage({ kind: "error", text: "JSON 格式不正确" });
      else report(setMessage, err);
    }
  }

  return <Panel title="价目与高峰窗口" description="用户价优先于 models.dev 目录价。provider 留空表示所有供应商，model 写 * 表示该供应商全部模型。供应商/上游密钥倍率只乘目录价；高峰系数按调用时间乘在最终单价上，用户价也一样。" actions={<button type="button" className="secondary" onClick={() => setOpen((value) => !value)}>{open ? "收起" : "编辑"}</button>}>
    {open ? <form className="stack" onSubmit={save}>
      <label>用户价目（JSON 数组）<textarea rows={8} value={prices} onChange={(e) => setPrices(e.target.value)} placeholder='[{"provider":"p1","model":"gpt-5","input":1.2,"output":8,"cacheRead":0.3,"cacheWrite":1.5,"cacheWrite1h":3}]'/></label>
      <label>高峰窗口（JSON 数组）<textarea rows={6} value={peaks} onChange={(e) => setPeaks(e.target.value)} placeholder='[{"provider":"p1","model":"*","start":"09:00","end":"18:00","factor":1.5}]'/></label>
      <div className="form-actions"><button type="submit">保存价目</button></div>
    </form> : <p className="form-note">用 JSON 写入用户价、供应商倍率（在提供方编辑里）和高峰窗口。价目只用于读取时计算，不写入 attempts.jsonl。</p>}
  </Panel>;
}

export function Usage({ tick, setMessage }) {
  const [period, setPeriod] = useState("all");
  const [routeId, setRouteId] = useState("");
  const [providerId, setProviderId] = useState("");
  const [agent, setAgent] = useState("");
  const [model, setModel] = useState("");
  const [callerKeyId, setCallerKeyId] = useState("");
  const [source, setSource] = useState("");
  const [start, setStart] = useState("");
  const [end, setEnd] = useState("");
  const [offset, setOffset] = useState(0);
  const [body, setBody] = useState(null);
  const [heat, setHeat] = useState(null);
  const [traffic, setTraffic] = useState(null);
  const [detail, setDetail] = useState(null);
  const [hideProvider, setHideProvider] = useState("");
  const [hideModel, setHideModel] = useState("");

  function filterQuery(includePeriod) {
    const query = new URLSearchParams();
    if (includePeriod && period) query.set("period", period);
    if (routeId.trim()) query.set("routeId", routeId.trim());
    if (providerId.trim()) query.set("providerId", providerId.trim());
    if (agent.trim()) query.set("agent", agent.trim());
    if (model.trim()) query.set("model", model.trim());
    if (callerKeyId.trim()) query.set("callerKeyId", callerKeyId.trim());
    if (source.trim()) query.set("source", source.trim());
    if (start.trim()) query.set("start", start.trim());
    if (end.trim()) query.set("end", end.trim());
    return query;
  }

  async function load(event, nextOffset = offset) {
    if (event) event.preventDefault();
    const usageQuery = filterQuery(true);
    usageQuery.set("offset", String(nextOffset));
    usageQuery.set("limit", String(pageSize));
    const heatQuery = filterQuery(false);
    try {
      const [usage, map, live] = await Promise.all([
        api("GET", "/usage?" + usageQuery),
        api("GET", "/usage/heatmap?" + heatQuery),
        api("GET", "/usage/traffic"),
      ]);
      setBody(usage);
      setHeat(map);
      setTraffic(live);
      setOffset(nextOffset);
    } catch (err) { report(setMessage, err); }
  }

  useEffect(() => { load(null, 0); }, [tick]);
  useEffect(() => {
    let current = true;
    async function poll() {
      try {
        const live = await api("GET", "/usage/traffic");
        if (current) setTraffic(live);
      } catch { /* the next manual refresh reports errors */ }
    }
    const timer = window.setInterval(poll, 5000);
    return () => { current = false; window.clearInterval(timer); };
  }, []);
  const records = body && body.status !== "unknown" ? (body.records || []) : [];
  const total = body && typeof body.total === "number" ? body.total : records.length;

  async function exportCSV() {
    const query = filterQuery(true);
    query.set("offset", String(offset));
    query.set("limit", String(pageSize));
    try { await download("/usage/requests.csv?" + query, "usage.csv"); }
    catch (err) { report(setMessage, err); }
  }

  async function openRecord(record) {
    if (!record.session) return;
    const query = new URLSearchParams();
    query.set("session", record.session);
    if (record.agent || record.caller) query.set("agent", record.agent || record.caller);
    const started = Date.parse(record.started);
    if (!Number.isNaN(started)) query.set("from", new Date(started - 1000).toISOString());
    const finished = Date.parse(record.finished);
    if (!Number.isNaN(finished)) {
      const end = record.handedOn ? Date.now() : finished + 60000;
      query.set("to", new Date(end).toISOString());
    }
    setDetail({ session: record.session, agent: record.agent || record.caller || "", body: null });
    try {
      const content = await api("GET", "/usage/requests/content?" + query);
      setDetail({ session: record.session, agent: record.agent || record.caller || "", body: content });
    } catch (err) { report(setMessage, err); setDetail(null); }
  }

  async function clearHistory(event) {
    event.preventDefault();
    if (!hideProvider.trim() || !hideModel.trim()) {
      setMessage({ kind: "error", text: "清除前要填写提供方 ID 和模型" });
      return;
    }
    try {
      await api("POST", "/usage/clear", { providerId: hideProvider.trim(), model: hideModel.trim() });
      setMessage({ kind: "note", text: "已隐藏这个提供方和模型在当前时刻之前的页面记录。文件不删，密钥上限仍统计。" });
      load(null, 0);
    } catch (err) { report(setMessage, err); }
  }

  return <div className="stack">
    <Panel title="筛选调用记录" description="窗口按这台机器的本地日历日。一次请求的 handedOn 行不计调用次数，token 和费用照计。来源为探针、MCP、搜索的行不进合计。">
      <form className="grid filters" onSubmit={(event) => load(event, 0)}>
        <label>窗口<select value={period} onChange={(e) => setPeriod(e.target.value)}>{periods.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        <label>Agent<input value={agent} onChange={(e) => setAgent(e.target.value)}/></label>
        <label>模型<input value={model} onChange={(e) => setModel(e.target.value)}/></label>
        <label>提供方 ID<input value={providerId} onChange={(e) => setProviderId(e.target.value)}/></label>
        <label>调用方密钥 ID<input value={callerKeyId} onChange={(e) => setCallerKeyId(e.target.value)}/></label>
        <label>来源<input value={source} placeholder="reported / probe / mcp / search" onChange={(e) => setSource(e.target.value)}/></label>
        <label>路由 ID<input value={routeId} onChange={(e) => setRouteId(e.target.value)}/></label>
        <label>开始时间<input value={start} placeholder="RFC3339" onChange={(e) => setStart(e.target.value)}/></label>
        <label>结束时间<input value={end} placeholder="RFC3339" onChange={(e) => setEnd(e.target.value)}/></label>
        <div className="form-actions form-wide"><button type="button" className="secondary" onClick={exportCSV}>导出 CSV</button><button type="submit">应用筛选</button></div>
      </form>
    </Panel>
    {body && body.status === "unknown" ? <p className="note standalone">用量存储不可读，不能当成 0。不会返回半份合计。</p> : <>
      <Stats summary={body && body.summary}/>
      <Traffic snapshot={traffic}/>
      <Series summary={body && body.summary} period={period}/>
      {heat && heat.status === "unknown" ? <p className="note standalone">热力图不可读，不能当成 0。</p> : null}
      <Heatmap days={heat && heat.days}/>
      <Groups summary={body && body.summary}/>
      <Pricing setMessage={setMessage}/>
      <Panel title="清除页面历史" description="只记下「这个提供方 + 模型在当前时刻之前不再显示」。文件不删，密钥限额和配额估算仍然统计这些行。">
        <form className="grid" onSubmit={clearHistory}>
          <label>提供方 ID<input value={hideProvider} onChange={(e) => setHideProvider(e.target.value)}/></label>
          <label>模型<input value={hideModel} onChange={(e) => setHideModel(e.target.value)}/></label>
          <div className="form-actions form-wide"><button type="submit" className="danger">清除此前记录</button></div>
        </form>
      </Panel>
      <Balances rows={body && body.balances}/>
      <Quotas rows={body && body.quotas}/>
      <Panel title="调用记录" description={`共 ${total} 条，当前显示 ${records.length} 条。新的在前。一次请求一行结局；已计费后换下一家的失败行另有标记。`} className="table-panel" actions={<div className="row-actions"><button type="button" className="secondary" disabled={offset === 0} onClick={() => load(null, Math.max(0, offset - pageSize))}>上一页</button><button type="button" className="secondary" disabled={offset + records.length >= total} onClick={() => load(null, offset + pageSize)}>下一页</button></div>}>
        <Table headers={["开始", "模型", "Agent", "会话", "提供方", "调用方密钥", "结果", "来源", "耗时", "用量", "操作"]} rows={records.map((record) => [
          record.started,
          <strong className="cell-title">{record.model || "—"}</strong>,
          record.agent || record.caller || "—",
          record.session || "—",
          record.providerId || "—",
          <div><code>{record.callerKeyId || record.tokenId || "—"}</code>{record.callerIssuerId ? <div className="form-note">签发：<code>{record.callerIssuerId}</code></div> : null}</div>,
          outcomeLabel(record),
          sourceLabel(record),
          duration(record),
          usageLabel(record),
          <button type="button" className="ghost" disabled={!record.session} title={record.session ? "打开这段会话的存档原文" : "这条调用没有会话标识"} onClick={() => openRecord(record)}>打开</button>,
        ])} empty="没有符合筛选条件的调用记录"/>
      </Panel>
      <ConversationDetail detail={detail} onClose={() => setDetail(null)}/>
    </>}
  </div>;
}
