import { useEffect, useState } from "react";
import { api } from "../api.js";
import { Panel, Table, Toggle, report } from "../components/ui.jsx";

export function Listeners({ tick, refresh, setMessage }) {
  const [data, setData] = useState(null);
  const [gateway, setGateway] = useState("");
  const [consoleAddr, setConsoleAddr] = useState("");
  const [share, setShare] = useState(false);

  useEffect(() => {
    api("GET", "/listeners").then((body) => {
      setData(body);
      setShare(!!body.share);
    }).catch((err) => report(setMessage, err));
  }, [tick]);

  async function save(event) {
    event.preventDefault();
    const body = { share };
    if (gateway.trim()) body.gateway = gateway.trim();
    if (consoleAddr.trim()) body.console = consoleAddr.trim();
    try {
      await api("PUT", "/listeners", body);
      setMessage({ kind: "note", text: "监听地址已写入。若地址变了，请用新的控制台地址打开此页。" });
      setGateway("");
      setConsoleAddr("");
      refresh();
    } catch (err) { report(setMessage, err); }
  }

  const rows = data ? ["gateway", "console"].map((name) => {
    const port = data[name];
    return [name === "gateway" ? "网关" : "控制台", port.configured || "默认", port.effective ? <code>{port.effective}</code> : "未监听", <span className={`pill ${port.listening ? "ok" : ""}`}>{port.listening ? "监听中" : "未监听"}</span>];
  }) : [];

  return <div className="stack">
    <Panel title="监听状态" description="配置值与实际生效地址"><Table headers={["端口", "配置值", "生效地址", "状态"]} rows={rows} empty="正在读取监听状态"/></Panel>
    <Panel title="修改监听" description="留空的地址不会被修改">
      <form className="grid" onSubmit={save}>
        <label>网关监听地址<input value={gateway} placeholder="例如 127.0.0.1:8080" onChange={(e) => setGateway(e.target.value)}/></label>
        <label>控制台监听地址<input value={consoleAddr} placeholder="例如 127.0.0.1:8081" onChange={(e) => setConsoleAddr(e.target.value)}/></label>
        <div className="form-wide"><Toggle label="对其他机器开放网关" hint="仅允许非回环网关地址在开启共享时监听" checked={share} onChange={(e) => setShare(e.target.checked)}/></div>
        <div className="form-actions"><button type="submit">保存监听配置</button></div>
      </form>
    </Panel>
  </div>;
}
