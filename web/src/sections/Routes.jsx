import { useEffect, useState } from "react";
import { api, asList, splitList } from "../api.js";
import { Panel, Table, Toggle, report } from "../components/ui.jsx";

export function Routes({ tick, refresh, setMessage }) {
  const [routes, setRoutes] = useState([]);
  const [providers, setProviders] = useState([]);
  const [model, setModel] = useState("");
  const [providerIds, setProviderIds] = useState("");
  const [eligible, setEligible] = useState("");
  const [limit, setLimit] = useState(false);

  useEffect(() => {
    Promise.all([api("GET", "/routes"), api("GET", "/providers")]).then(([routeBody, providerBody]) => {
      setRoutes(asList(routeBody));
      setProviders(asList(providerBody));
    }).catch((err) => report(setMessage, err));
  }, [tick]);

  async function save(event) {
    event.preventDefault();
    const body = { modelMatch: model.trim(), providerIds: splitList(providerIds) };
    if (limit) body.eligible = splitList(eligible);
    try {
      await api("POST", "/routes", body);
      setModel("");
      setProviderIds("");
      setEligible("");
      setLimit(false);
      setMessage(null);
      refresh();
    } catch (err) { report(setMessage, err); }
  }

  return <div className="stack">
    <Panel title="新建路由" description="模型名完全匹配后，按提供方顺序依次尝试">
      <form className="grid" onSubmit={save}>
        <label>模型匹配<input value={model} placeholder="与请求中的模型名完全一致" onChange={(e) => setModel(e.target.value)}/></label>
        <label>提供方顺序<input value={providerIds} placeholder={providers.length ? `可用：${providers.map((p) => p.id).join(", ")}` : "提供方 ID，逗号分隔"} onChange={(e) => setProviderIds(e.target.value)}/></label>
        <label>降级类别<input value={eligible} placeholder="默认仅 upstream_unavailable" onChange={(e) => setEligible(e.target.value)}/></label>
        <div className="form-wide"><Toggle label="写入 eligible" hint="开启后使用上方类别；列表为空表示不降级" checked={limit} onChange={(e) => setLimit(e.target.checked)}/></div>
        <div className="form-actions"><button type="submit">新建路由</button></div>
      </form>
    </Panel>
    <Panel title="路由列表" description={`${routes.length} 条路由`}><Table headers={["ID", "模型", "提供方", "eligible", "操作"]} rows={routes.map((route) => [
      <code>{route.id}</code>, <strong className="cell-title">{route.modelMatch}</strong>, (route.providerIds || []).join(", ") || "—",
      route.eligible ? (route.eligible.length ? route.eligible.join(", ") : "空，不降级") : "默认",
      <button type="button" className="danger" onClick={async () => { try { await api("DELETE", "/routes/" + route.id); refresh(); } catch (err) { report(setMessage, err); } }}>删除</button>,
    ])} empty="还没有路由"/></Panel>
  </div>;
}
