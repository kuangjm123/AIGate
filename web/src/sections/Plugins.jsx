import { useEffect, useState } from "react";
import { api, asList } from "../api.js";
import { Panel, Table, Toggle, report } from "../components/ui.jsx";

export function Plugins({ tick, refresh, setMessage }) {
  const [list, setList] = useState([]);
  const [enabled, setEnabled] = useState(true);

  useEffect(() => { api("GET", "/plugins").then((body) => setList(asList(body))).catch((err) => report(setMessage, err)); }, [tick]);

  async function create(event) {
    event.preventDefault();
    try { await api("POST", "/plugins", { enabled }); setMessage(null); refresh(); }
    catch (err) { report(setMessage, err); }
  }

  async function reorder(ids) {
    try { await api("PUT", "/plugins/order", { ids }); refresh(); }
    catch (err) { report(setMessage, err); }
  }

  function move(index, offset) {
    const ids = list.map((item) => item.id);
    [ids[index + offset], ids[index]] = [ids[index], ids[index + offset]];
    reorder(ids);
  }

  return <div className="stack">
    <div className="note standalone">这里只保存插件的启用状态和执行顺序，进程不会加载插件代码。</div>
    <Panel title="新建插件记录"><form className="grid compact" onSubmit={create}><div className="form-wide"><Toggle label="创建时启用" checked={enabled} onChange={(e) => setEnabled(e.target.checked)}/></div><div className="form-actions"><button type="submit">新建插件记录</button></div></form></Panel>
    <Panel title="执行顺序" description={`${list.length} 条记录`}><Table headers={["顺序", "ID", "状态", "操作"]} rows={list.map((plugin, index) => [
      <span className="order-number">{String(plugin.order).padStart(2, "0")}</span>, <code>{plugin.id}</code>, <span className={`pill ${plugin.enabled ? "ok" : ""}`}>{plugin.enabled ? "已启用" : "已禁用"}</span>,
      <span className="row-actions"><button type="button" className="secondary" disabled={index === 0} onClick={() => move(index, -1)}>上移</button><button type="button" className="secondary" disabled={index === list.length - 1} onClick={() => move(index, 1)}>下移</button><button type="button" className="secondary" onClick={async () => { try { await api("PUT", "/plugins/" + plugin.id, { enabled: !plugin.enabled }); refresh(); } catch (err) { report(setMessage, err); } }}>{plugin.enabled ? "禁用" : "启用"}</button></span>,
    ])} empty="还没有插件记录"/></Panel>
  </div>;
}
