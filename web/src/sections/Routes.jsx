import { useEffect, useState } from "react";
import { api, asList, splitList } from "../api.js";
import { Panel, Table, Toggle, report } from "../components/ui.jsx";

const strategies = [
  ["", "Smart（默认）"],
  ["order", "按顺序"],
  ["rotate", "轮转"],
  ["usage", "最少使用"],
  ["pace", "速率优先"],
  ["weight", "加权"],
  ["free", "免费优先"],
  ["fast", "速度优先"],
  ["cheap", "最便宜"],
  ["drain", "用尽再换"],
  ["expiring", "免费快过期"],
];

const routeStrategies = strategies.filter(([id]) => id !== "cheap" && id !== "expiring");

function strategyLabel(id) {
  const found = strategies.find(([value]) => value === (id || ""));
  return found ? found[1] : id;
}

function parseWeights(text) {
  const out = {};
  for (const part of splitList(text)) {
    const eq = part.indexOf("=");
    if (eq <= 0) continue;
    const weight = Number(part.slice(eq + 1).trim());
    if (Number.isInteger(weight) && weight >= 0) out[part.slice(0, eq).trim()] = weight;
  }
  return out;
}

function weightText(weights) {
  return Object.entries(weights || {}).map(([id, weight]) => `${id}=${weight}`).join(", ");
}

function groupPath(id) {
  return "/groups/" + encodeURIComponent(id);
}

function routePath(id) {
  return "/routes/" + encodeURIComponent(id);
}

export function Routes({ tick, refresh, setMessage }) {
  const [routes, setRoutes] = useState([]);
  const [providers, setProviders] = useState([]);
  const [groups, setGroups] = useState([]);
  const [autoGroups, setAutoGroups] = useState(true);
  const [model, setModel] = useState("");
  const [providerIds, setProviderIds] = useState("");
  const [routing, setRouting] = useState("");
  const [weights, setWeights] = useState("");
  const [eligible, setEligible] = useState("");
  const [limit, setLimit] = useState(false);
  const [groupID, setGroupID] = useState("");
  const [members, setMembers] = useState("");
  const [groupRouting, setGroupRouting] = useState("");
  const [groupWeights, setGroupWeights] = useState("");
  const [groupEligible, setGroupEligible] = useState("");
  const [groupLimit, setGroupLimit] = useState(false);
  const [editingRoute, setEditingRoute] = useState("");
  const [editingGroup, setEditingGroup] = useState("");

  useEffect(() => {
    Promise.all([api("GET", "/routes"), api("GET", "/providers"), api("GET", "/groups"), api("GET", "/auto-groups")])
      .then(([routeBody, providerBody, groupBody, autoBody]) => {
        setRoutes(asList(routeBody));
        setProviders(asList(providerBody));
        setGroups(asList(groupBody));
        setAutoGroups(autoBody?.on !== false);
      })
      .catch((err) => report(setMessage, err));
  }, [tick]);

  const memberHints = providers.length
    ? providers.flatMap((provider) => (provider.models || []).slice(0, 4).map((name) => `${provider.id}/${name}`))
    : [];

  function clearRoute() {
    setEditingRoute("");
    setModel("");
    setProviderIds("");
    setRouting("");
    setWeights("");
    setEligible("");
    setLimit(false);
  }

  function clearGroup() {
    setEditingGroup("");
    setGroupID("");
    setMembers("");
    setGroupRouting("");
    setGroupWeights("");
    setGroupEligible("");
    setGroupLimit(false);
  }

  async function saveRoute(event) {
    event.preventDefault();
    const modelMatch = model.trim();
    if (!modelMatch || modelMatch.includes(",")) {
      setMessage({ kind: "error", text: "一个路由只匹配一个完整模型名，不能为空，也不能用逗号写多个模型。" });
      return;
    }
    const body = { modelMatch, providerIds: splitList(providerIds) };
    if (routing) body.routing = routing;
    if (routing === "weight") body.weights = parseWeights(weights);
    if (limit) body.eligible = splitList(eligible);
    try {
      if (editingRoute) {
        await api("PUT", routePath(editingRoute), body);
      } else {
        await api("POST", "/routes", body);
      }
      clearRoute();
      setMessage(null);
      refresh();
    } catch (err) { report(setMessage, err); }
  }

  function editRoute(route) {
    setEditingRoute(route.id);
    setModel(route.modelMatch || "");
    setProviderIds((route.providerIds || []).join(", "));
    setRouting(route.routing || "");
    setWeights(weightText(route.weights));
    setEligible((route.eligible || []).join(", "));
    setLimit(Boolean(route.eligible));
  }

  function editGroup(group) {
    setEditingGroup(group.id);
    setGroupID(group.id);
    setMembers((group.members || []).join(", "));
    setGroupRouting(group.routing || "");
    setGroupWeights(weightText(group.weights));
    setGroupEligible((group.eligible || []).join(", "));
    setGroupLimit(Boolean(group.eligible));
  }

  async function saveGroup(event) {
    event.preventDefault();
    const body = { members: splitList(members) };
    if (groupRouting) body.routing = groupRouting;
    if (groupRouting === "weight") body.weights = parseWeights(groupWeights);
    if (groupLimit) body.eligible = splitList(groupEligible);
    try {
      if (editingGroup) {
        await api("PUT", groupPath(editingGroup), body);
      } else {
        body.id = groupID.trim();
        await api("POST", "/groups", body);
      }
      clearGroup();
      setMessage(null);
      refresh();
    } catch (err) { report(setMessage, err); }
  }

  async function saveAutoGroups(on) {
    try {
      const saved = await api("PUT", "/auto-groups", { on });
      setAutoGroups(saved.on !== false);
      refresh();
    } catch (err) { report(setMessage, err); }
  }

  return <div className="stack">
    <Panel title={editingRoute ? `编辑路由 ${editingRoute}` : "新建路由"} description="模型名完全匹配后，由所选策略决定候选提供方的尝试顺序" actions={editingRoute ? <button type="button" className="ghost" onClick={clearRoute}>取消编辑</button> : null}>
      <form className="grid" onSubmit={saveRoute}>
        <label>模型匹配<input value={model} required placeholder="与请求中的模型名完全一致" onChange={(e) => setModel(e.target.value)}/><small className="field-hint">一个路由只写一个完整模型名，不能用逗号写多个。</small></label>
        <label>提供方顺序<input value={providerIds} placeholder={providers.length ? `可用：${providers.map((p) => p.id).join(", ")}` : "提供方 ID，逗号分隔"} onChange={(e) => setProviderIds(e.target.value)}/></label>
        <label>策略<select value={routing} onChange={(e) => setRouting(e.target.value)}>{routeStrategies.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
        {routing === "weight" && <label>权重<input value={weights} placeholder="提供方=权重，如 p1=3, p2=1" onChange={(e) => setWeights(e.target.value)}/></label>}
        <label>降级类别<input value={eligible} placeholder="默认仅 upstream_unavailable" onChange={(e) => setEligible(e.target.value)}/></label>
        <div className="form-wide"><Toggle label="写入 eligible" hint="开启后使用上方类别；列表为空表示不降级" checked={limit} onChange={(e) => setLimit(e.target.checked)}/></div>
        <div className="form-actions"><button type="submit">{editingRoute ? "更新路由" : "新建路由"}</button></div>
      </form>
    </Panel>
    <Panel title="路由列表" description={`${routes.length} 条路由`}><Table headers={["ID", "模型", "提供方", "策略", "eligible", "操作"]} rows={routes.map((route) => [
      <code>{route.id}</code>, <strong className="cell-title">{route.modelMatch}</strong>, (route.providerIds || []).join(", ") || "—",
      strategyLabel(route.routing) + (route.weights ? "（" + weightText(route.weights) + "）" : ""),
      route.eligible ? (route.eligible.length ? route.eligible.join(", ") : "空，不降级") : "默认",
      <span className="row-actions"><button type="button" className="secondary" onClick={() => editRoute(route)}>编辑</button><button type="button" className="danger" onClick={async () => { try { await api("DELETE", routePath(route.id)); refresh(); } catch (err) { report(setMessage, err); } }}>删除</button></span>,
    ])} empty="还没有路由"/></Panel>

    <Panel title={editingGroup ? `编辑组 ${editingGroup}` : "新建路由组"} description="成员写 提供方/模型 或 group/组ID。组名就是请求里的模型名；cheap 和 expiring 只能配在组上" actions={editingGroup ? <button type="button" className="ghost" onClick={clearGroup}>取消编辑</button> : null}>
      <form className="grid" onSubmit={saveGroup}>
        <label>组 ID<input value={groupID} disabled={Boolean(editingGroup)} placeholder="例如 strong-models" onChange={(e) => setGroupID(e.target.value)}/></label>
        <label>策略<select value={groupRouting} onChange={(e) => setGroupRouting(e.target.value)}>{strategies.map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select><small className="field-hint">留空时外层可继承；外层 order/weight 时内层按 Smart。</small></label>
        <label className="form-wide">成员<input value={members} placeholder={memberHints.length ? `例如 ${memberHints.slice(0, 2).join(", ")}` : "提供方/模型，逗号分隔；嵌套组写 group/组ID"} onChange={(e) => setMembers(e.target.value)}/></label>
        {groupRouting === "weight" && <label className="form-wide">权重<input value={groupWeights} placeholder="成员=权重，如 p1/model=3, p2/model=1" onChange={(e) => setGroupWeights(e.target.value)}/></label>}
        <label>降级类别<input value={groupEligible} placeholder="默认仅 upstream_unavailable" onChange={(e) => setGroupEligible(e.target.value)}/></label>
        <div className="form-wide"><Toggle label="写入 eligible" hint="组内成员可以在首字节前按这些类别继续尝试" checked={groupLimit} onChange={(e) => setGroupLimit(e.target.checked)}/></div>
        <div className="form-actions"><button type="submit">{editingGroup ? "更新组" : "新建组"}</button></div>
      </form>
    </Panel>
    <Panel title="路由组列表" description={`${groups.length} 个组`} actions={<Toggle label="自动组" hint="同一个裸模型名被多家列出时自动合成一组" checked={autoGroups} onChange={(e) => saveAutoGroups(e.target.checked)}/>}><Table headers={["组 ID", "成员", "策略", "eligible", "操作"]} rows={groups.map((group) => [
      <strong className="cell-title">{group.id}</strong>,
      (group.members || []).join(", ") || "—",
      strategyLabel(group.routing) + (group.weights ? "（" + weightText(group.weights) + "）" : ""),
      group.eligible ? (group.eligible.length ? group.eligible.join(", ") : "空，不降级") : "默认",
      <span className="row-actions"><button type="button" className="secondary" onClick={() => editGroup(group)}>编辑</button><button type="button" className="danger" onClick={async () => { try { await api("DELETE", groupPath(group.id)); refresh(); } catch (err) { report(setMessage, err); } }}>删除</button></span>,
    ])} empty="还没有路由组"/></Panel>
  </div>;
}
