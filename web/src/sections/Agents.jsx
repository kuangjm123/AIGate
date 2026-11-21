import { useEffect, useState } from "react";
import { api, asList } from "../api.js";
import { Panel, Table, report } from "../components/ui.jsx";

const agentState = {
  absent: "未安装",
  off: "未接入",
  on: "已接入",
  drift: "配置已变化",
  hand: "已指向网关",
  unknown: "配置不可读",
  manual: "需手动",
};

function agentError(err) {
  switch (err.message) {
    case "no_models": return "还没有可提供的模型。先添加提供方或路由，再接入。";
    case "not_listening": return "网关没有在监听，没有改应用的配置。";
    case "unknown": return "应用的配置读不出来，没有改它。";
    case "absent": return "没有检测到这个应用。";
    case "unsupported": return "这个应用要在它自己的界面里填写网关地址。";
    case "not_connected": return "这个应用不是从这里接入的，没有改它的配置。";
    case "id_taken": return "这些模型名已经在这个应用的配置里，没有改它。";
    default: return err.status === 401 ? "请先登录" : "操作没有完成";
  }
}

export function Agents({ tick, setMessage }) {
  const [rows, setRows] = useState([]);
  const [gateway, setGateway] = useState("");
  const [busy, setBusy] = useState("");
  const [known, setKnown] = useState(true);

  function load() {
    api("GET", "/agents").then((body) => {
      if (body.status !== "ok") {
        setKnown(false);
        setRows([]);
        return;
      }
      setKnown(true);
      setGateway(body.gateway || "");
      setRows(asList(body.agents));
    }).catch((err) => report(setMessage, err));
  }

  useEffect(() => { load(); }, [tick]);

  async function run(id, action) {
    setBusy(id + action);
    setMessage(null);
    try {
      await api("POST", "/agents/" + id + "/" + action);
      setMessage({ kind: "note", text: action === "connect" ? "已接入。请重新打开这个应用，之后它的模型请求会发给网关。" : "已断开，应用配置已恢复到接入前。" });
      load();
    } catch (err) {
      setMessage({ kind: "error", text: agentError(err) });
    } finally {
      setBusy("");
    }
  }

  if (!known) return <p className="status">读不到这台电脑上的用户目录，没有检测应用。</p>;

  const table = rows.map((row) => {
    const actions = [];
    if (row.support === "wire" && row.installed && row.state !== "unknown") {
      if (row.state === "on") actions.push(["disconnect", "断开"]);
      else if (row.state === "drift") actions.push(["connect", "重新接入"], ["disconnect", "断开"]);
      else if (row.state === "off" || row.state === "hand") actions.push(["connect", "接入"]);
    }
    const stateClass = row.state === "on" ? "ok" : ["unknown", "drift"].includes(row.state) ? "bad" : "";
    return [
      <strong className="cell-title">{row.name}</strong>,
      <span className={`pill ${stateClass}`}>{agentState[row.state] || row.state}</span>,
      <><div>{row.where || "—"}</div>{row.model ? <div className="status">模型 {row.model}</div> : null}{row.note ? <div className="status">{row.note}</div> : null}</>,
      <span className="row-actions">{actions.length ? actions.map(([action, label]) => <button key={action} type="button" className={action === "disconnect" ? "danger" : "secondary"} disabled={busy !== ""} onClick={() => run(row.id, action)}>{busy === row.id + action ? "处理中" : label}</button>) : <span className="muted">—</span>}</span>,
    ];
  });

  return <>
    <div className="info-strip"><span><strong>自动配置，随时可恢复</strong><small>接入会修改应用自身配置；断开后恢复接入前的值。部分应用需要重新打开。</small></span><code>{gateway || "网关未监听"}</code></div>
    <Panel title="本机 Coding Agent" description={`检测到 ${rows.length} 个应用`}><Table headers={["应用", "状态", "配置", "操作"]} rows={table} empty="未检测到应用"/></Panel>
  </>;
}
