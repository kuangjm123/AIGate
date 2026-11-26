import { useEffect, useState } from "react";
import { api, asList, splitList } from "../api.js";
import { Panel, Table, report } from "../components/ui.jsx";

const windows = [
  ["today", "今天"],
  ["week", "本周（周一起）"],
  ["month", "本月（1 日起）"],
  ["hours", "最近 N 小时"],
  ["days", "最近 N 天"],
  ["all", "全部"],
];

const legacyPeriods = [["30d", "近 30 天"], ["today", "今天"], ["7d", "近 7 天"], ["all", "全部"]];

function datetimeLocal(value) {
  if (!value) return "";
  const at = new Date(value);
  if (Number.isNaN(at.getTime())) return "";
  const pad = (n) => String(n).padStart(2, "0");
  return `${at.getFullYear()}-${pad(at.getMonth() + 1)}-${pad(at.getDate())}T${pad(at.getHours())}:${pad(at.getMinutes())}:${pad(at.getSeconds())}`;
}

function blankLimit() {
  return { usd: "", tokens: "", window: "month", hours: "", days: "", since: "", provider: "", model: "" };
}

function limitLabel(limit) {
  const parts = [];
  if (typeof limit.usd === "number" && limit.usd > 0) parts.push("$" + limit.usd);
  if (typeof limit.tokens === "number" && limit.tokens > 0) parts.push(limit.tokens + " token");
  if (!parts.length) parts.push("不封顶");
  const win = windows.find(([value]) => value === limit.window);
  let windowLabel = win ? win[1] : limit.window;
  if (limit.window === "hours") windowLabel = "最近 " + limit.hours + " 小时";
  if (limit.window === "days") windowLabel = "最近 " + limit.days + " 天";
  if ((limit.window === "hours" || limit.window === "days") && datetimeLocal(limit.since)) {
    windowLabel = "从 " + datetimeLocal(limit.since).replace("T", " ") + " 起";
  }
  let scope = "";
  if (limit.provider) scope += " · " + limit.provider;
  if (limit.model) scope += " · " + limit.model;
  return parts.join(" + ") + " / " + windowLabel + scope;
}

function legacyLabel(token) {
  if (typeof token.spendLimit !== "number") return "";
  const period = legacyPeriods.find(([value]) => value === token.spendPeriod);
  return "旧上限 $" + token.spendLimit + (period ? " / " + period[1] : "");
}

function limitsOf(token) {
  return (token.limits || []).map((limit) => ({
    usd: limit.usd == null ? "" : String(limit.usd),
    tokens: limit.tokens == null ? "" : String(limit.tokens),
    window: limit.window || "month",
    hours: limit.hours ? String(limit.hours) : "",
    days: limit.days ? String(limit.days) : "",
    since: datetimeLocal(limit.since),
    provider: limit.provider || "",
    model: limit.model || "",
  }));
}

function toLimits(rows) {
  const out = [];
  for (const row of rows) {
    const usdText = row.usd.trim();
    const tokenText = row.tokens.trim();
    const usd = usdText === "" ? null : Number(usdText);
    const tokens = tokenText === "" ? null : Number(tokenText);
    if ((usd == null || usd === 0) && (tokens == null || tokens === 0)) continue;
    if ((usd != null && (!Number.isFinite(usd) || usd < 0)) ||
        (tokens != null && (!Number.isFinite(tokens) || tokens < 0 || !Number.isInteger(tokens)))) {
      throw new Error("限额的美元要写成非负数字，token 要写成非负整数");
    }
    const limit = { window: row.window };
    if (usd != null) limit.usd = usd;
    if (tokens != null) limit.tokens = tokens;
    if (row.window === "hours" || row.window === "days") {
      const count = Number(row.window === "hours" ? row.hours : row.days);
      if (!Number.isInteger(count) || count <= 0) {
        throw new Error(row.window === "hours" ? "最近 N 小时要是正整数" : "最近 N 天要是正整数");
      }
      limit[row.window === "hours" ? "hours" : "days"] = count;
      if ((row.since || "").trim()) {
        const when = new Date(row.since.trim());
        if (Number.isNaN(when.getTime())) throw new Error("起算时刻格式不正确");
        limit.since = when.toISOString();
      }
    }
    if (row.provider.trim()) limit.provider = row.provider.trim();
    if (row.model.trim()) limit.model = row.model.trim();
    out.push(limit);
  }
  return out;
}

function LimitsEditor({ rows, setRows }) {
  function update(index, patch) {
    setRows(rows.map((row, i) => i === index ? { ...row, ...patch } : row));
  }
  return <div className="limit-editor">
    <div className="form-section-title"><span>花费限额（最多 5 条）</span><small>美元与 token 可同时卡；0 或留空表示这一项不封顶；短时窗口可填起算时刻</small></div>
    {rows.map((row, index) => <div className="limit-row" key={index}>
      <label>美元<input value={row.usd} inputMode="decimal" placeholder="不限" onChange={(e) => update(index, { usd: e.target.value })}/></label>
      <label>token<input value={row.tokens} inputMode="numeric" placeholder="不限" onChange={(e) => update(index, { tokens: e.target.value })}/></label>
      <label>窗口<select value={row.window} onChange={(e) => update(index, { window: e.target.value })}>{windows.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
      {row.window === "hours" ? <label>小时数<input value={row.hours} inputMode="numeric" onChange={(e) => update(index, { hours: e.target.value })}/></label> : row.window === "days" ? <label>天数<input value={row.days} inputMode="numeric" onChange={(e) => update(index, { days: e.target.value })}/></label> : <span/>}
      {row.window === "hours" || row.window === "days" ? <label>起算时刻<input type="datetime-local" step="1" value={row.since} onChange={(e) => update(index, { since: e.target.value })}/></label> : <span/>}
      <label>提供方 ID<input value={row.provider} placeholder="全部" onChange={(e) => update(index, { provider: e.target.value })}/></label>
      <label>模型<input value={row.model} placeholder="全部" onChange={(e) => update(index, { model: e.target.value })}/></label>
      <button type="button" className="ghost" onClick={() => setRows(rows.filter((_, i) => i !== index))}>移除</button>
    </div>)}
    <button type="button" className="secondary fit-content" disabled={rows.length >= 5} onClick={() => setRows([...rows, blankLimit()])}>+ 添加一条限额</button>
  </div>;
}

export function Tokens({ tick, refresh, setMessage }) {
  const [list, setList] = useState([]);
  const [models, setModels] = useState("");
  const [issuerId, setIssuerId] = useState("");
  const [limits, setLimits] = useState([]);
  const [editing, setEditing] = useState("");
  const [editLimits, setEditLimits] = useState([]);

  useEffect(() => { api("GET", "/tokens").then((body) => setList(asList(body))).catch((err) => report(setMessage, err)); }, [tick]);

  async function create(event) {
    event.preventDefault();
    try {
      const body = { models: splitList(models), limits: toLimits(limits), issuerId: issuerId.trim() };
      const created = await api("POST", "/tokens", body);
      setModels("");
      setIssuerId("");
      setLimits([]);
      setMessage({ kind: "secret", text: "令牌只显示这一次：" + created.secret });
      refresh();
    } catch (err) { report(setMessage, err); }
  }

  function edit(token) {
    setEditing(token.id);
    setEditLimits(limitsOf(token));
  }

  async function saveLimits(event) {
    event.preventDefault();
    try {
      await api("PUT", "/tokens/" + editing, { limits: toLimits(editLimits) });
      setMessage({ kind: "note", text: "限额已保存。下一次拨号前按新窗口加总。" });
      setEditing("");
      setEditLimits([]);
      refresh();
    } catch (err) { report(setMessage, err); }
  }

  return <div className="stack">
    <Panel title="创建令牌" description="可限定令牌能够查看和调用的模型。限额按读取时价目把已发生的调用加成美元；token 限额数输入、输出、缓存读写之和。达到任意一条就拒绝下一次拨号。">
      <form className="stack" onSubmit={create}>
        <label>可用模型<input value={models} placeholder="模型名，逗号分隔；留空表示全部" onChange={(e) => setModels(e.target.value)}/></label>
        <label>签发密钥 ID<input value={issuerId} placeholder="短时密钥填签发它的网关令牌 id；留空表示独立密钥" onChange={(e) => setIssuerId(e.target.value)}/><small className="field-hint">短时密钥的 token 和费用同时算进签发它的那把密钥的上限。</small></label>
        <LimitsEditor rows={limits} setRows={setLimits}/>
        <div className="form-actions"><button type="submit">创建网关令牌</button></div>
      </form>
    </Panel>
    {editing ? <Panel title={"编辑限额 · " + editing} description="写空数组即清空全部限额。旧 spendLimit 仍会生效，需在配置里清除。" actions={<button type="button" className="ghost" onClick={() => { setEditing(""); setEditLimits([]); }}>取消</button>}>
      <form className="stack" onSubmit={saveLimits}>
        <LimitsEditor rows={editLimits} setRows={setEditLimits}/>
        <div className="form-actions"><button type="submit">保存限额</button></div>
      </form>
    </Panel> : null}
    <Panel title="令牌列表" description="出于安全考虑，列表只显示掩码" className="table-panel">
      <Table headers={["ID", "掩码", "可用模型", "花费限额", "状态", "操作"]} rows={list.map((token) => [
        <div><code>{token.id}</code>{token.issuerId ? <div className="form-note">签发：<code>{token.issuerId}</code></div> : null}</div>,
        <code>{token.mask}</code>,
        (token.models || []).join(", ") || "全部模型",
        <span className="limit-list">{(token.limits || []).map((limit, index) => <span className="pill" key={index}>{limitLabel(limit)}</span>)}{legacyLabel(token) ? <span className="pill">{legacyLabel(token)}</span> : null}{!(token.limits || []).length && !legacyLabel(token) ? "不限" : null}</span>,
        <span className={`pill ${token.revoked ? "bad" : "ok"}`}>{token.revoked ? "已撤销" : "有效"}</span>,
        token.revoked ? <span className="muted">—</span> : <span className="row-actions"><button type="button" className="secondary" onClick={() => edit(token)}>编辑限额</button><button type="button" className="danger" onClick={async () => { try { await api("POST", "/tokens/" + token.id + "/revoke"); setMessage(null); refresh(); } catch (err) { report(setMessage, err); } }}>撤销</button></span>,
      ])} empty="还没有网关令牌"/>
    </Panel>
  </div>;
}
