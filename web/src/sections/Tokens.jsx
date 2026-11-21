import { useEffect, useState } from "react";
import { api, asList, splitList } from "../api.js";
import { Panel, Table, report } from "../components/ui.jsx";

export function Tokens({ tick, refresh, setMessage }) {
  const [list, setList] = useState([]);
  const [models, setModels] = useState("");

  useEffect(() => { api("GET", "/tokens").then((body) => setList(asList(body))).catch((err) => report(setMessage, err)); }, [tick]);

  async function create(event) {
    event.preventDefault();
    try {
      const created = await api("POST", "/tokens", { models: splitList(models) });
      setModels("");
      setMessage({ kind: "secret", text: "令牌只显示这一次：" + created.secret });
      refresh();
    } catch (err) { report(setMessage, err); }
  }

  return <div className="stack">
    <Panel title="创建令牌" description="可限定令牌能够查看和调用的模型；留空表示不限制">
      <form className="grid inline-form" onSubmit={create}><label>可用模型<input value={models} placeholder="模型名，逗号分隔" onChange={(e) => setModels(e.target.value)}/></label><button type="submit">创建网关令牌</button></form>
    </Panel>
    <Panel title="令牌列表" description="出于安全考虑，列表只显示掩码"><Table headers={["ID", "掩码", "可用模型", "状态", "操作"]} rows={list.map((token) => [
      <code>{token.id}</code>, <code>{token.mask}</code>, (token.models || []).join(", ") || "全部模型", <span className={`pill ${token.revoked ? "bad" : "ok"}`}>{token.revoked ? "已撤销" : "有效"}</span>,
      token.revoked ? <span className="muted">—</span> : <button type="button" className="danger" onClick={async () => { try { await api("POST", "/tokens/" + token.id + "/revoke"); setMessage(null); refresh(); } catch (err) { report(setMessage, err); } }}>撤销</button>,
    ])} empty="还没有网关令牌"/></Panel>
  </div>;
}
