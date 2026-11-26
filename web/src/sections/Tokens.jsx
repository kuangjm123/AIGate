import { useEffect, useState } from "react";
import { api, asList, splitList } from "../api.js";
import { Panel, Table, report } from "../components/ui.jsx";

const spendPeriods = [["30d", "近 30 天"], ["today", "今天"], ["7d", "近 7 天"], ["all", "全部"]];

function spendLabel(token) {
  if (typeof token.spendLimit !== "number") return "不限";
  const period = spendPeriods.find(([value]) => value === token.spendPeriod);
  return "$" + token.spendLimit + (period ? " / " + period[1] : "");
}

export function Tokens({ tick, refresh, setMessage }) {
  const [list, setList] = useState([]);
  const [models, setModels] = useState("");
  const [spend, setSpend] = useState("");
  const [period, setPeriod] = useState("30d");

  useEffect(() => { api("GET", "/tokens").then((body) => setList(asList(body))).catch((err) => report(setMessage, err)); }, [tick]);

  async function create(event) {
    event.preventDefault();
    const body = { models: splitList(models) };
    if (spend.trim()) {
      body.spendLimit = Number(spend);
      body.spendPeriod = period;
    }
    try {
      const created = await api("POST", "/tokens", body);
      setModels("");
      setSpend("");
      setMessage({ kind: "secret", text: "令牌只显示这一次：" + created.secret });
      refresh();
    } catch (err) { report(setMessage, err); }
  }

  return <div className="stack">
    <Panel title="创建令牌" description="可限定令牌能够查看和调用的模型。花费上限按价目把已发生的调用加成美元，达到后拒绝下一次调用。">
      <form className="grid" onSubmit={create}><label>可用模型<input value={models} placeholder="模型名，逗号分隔" onChange={(e) => setModels(e.target.value)}/></label><label>花费上限（美元）<input value={spend} placeholder="留空表示不限" inputMode="decimal" onChange={(e) => setSpend(e.target.value)}/></label><label>上限窗口<select value={period} onChange={(e) => setPeriod(e.target.value)}>{spendPeriods.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label><div className="form-actions"><button type="submit">创建网关令牌</button></div></form>
    </Panel>
    <Panel title="令牌列表" description="出于安全考虑，列表只显示掩码"><Table headers={["ID", "掩码", "可用模型", "花费上限", "状态", "操作"]} rows={list.map((token) => [
      <code>{token.id}</code>, <code>{token.mask}</code>, (token.models || []).join(", ") || "全部模型", spendLabel(token), <span className={`pill ${token.revoked ? "bad" : "ok"}`}>{token.revoked ? "已撤销" : "有效"}</span>,
      token.revoked ? <span className="muted">—</span> : <button type="button" className="danger" onClick={async () => { try { await api("POST", "/tokens/" + token.id + "/revoke"); setMessage(null); refresh(); } catch (err) { report(setMessage, err); } }}>撤销</button>,
    ])} empty="还没有网关令牌"/></Panel>
  </div>;
}
