import { useState } from "react";
import { api, asList } from "../api.js";
import { Panel, Table, Toggle, report } from "../components/ui.jsx";

export function SubscriptionAccounts({ accounts, refresh, setMessage }) {
  const [build, setBuild] = useState("workbuddy");
  const [apiKey, setApiKey] = useState("");
  const [blob, setBlob] = useState("");
  if (!accounts) return null;

  async function toggle(on) {
    try {
      await api("PUT", "/accounts/discovery", { on });
      refresh();
      setMessage({ kind: "note", text: on ? "已开始读取本机订阅登录。" : "已停止读取本机订阅登录。" });
    } catch (err) { report(setMessage, err); }
  }

  async function submit(body, note) {
    try {
      await api("POST", "/accounts/workbuddy", body);
      setApiKey("");
      setBlob("");
      refresh();
      setMessage({ kind: "note", text: note });
    } catch (err) { report(setMessage, err); }
  }

  function sourceOf(account) {
    if (account.plugin) return "插件占用";
    if (!account.readable) return "读不出来";
    if (account.origin === "desktop") return "桌面登录";
    if (account.origin === "saved") return "网关保存的登录";
    return "本机登录";
  }

  const rows = accounts.status === "ok" ? asList(accounts.accounts).map((account) => [account.name, sourceOf(account), account.modelsKnown ? (account.models || []).join(", ") || "没有模型" : "名单未知"]) : [];
  return <Panel title="订阅账号" description="复用本机订阅登录，或导入 WorkBuddy 凭据">
    <div className="stack">
      <p className="section-intro">桌面应用登录仅以只读方式使用，不保存刷新令牌。API Key 和导入账号保存在网关自己的登录记录中。</p>
      <Toggle label="读取本机订阅登录" hint="关闭后不再读取本机登录文件" checked={!!accounts.on} onChange={(e) => toggle(e.target.checked)}/>
      {accounts.status === "unknown" ? <p className="error standalone">本机登录暂时读不出来。</p> : null}
      {accounts.status === "off" ? <p className="status">账号发现已关闭。</p> : null}
      {accounts.status === "ok" ? <Table headers={["应用", "来源", "模型"]} rows={rows} empty="没有读到已登录的 Agent"/> : null}
      {accounts.on ? <div className="account-tools">
        <div className="subsection-heading"><h4>添加 WorkBuddy 账号</h4><p>通过 API Key 登录，或导入桌面登录 JSON。</p></div>
        <form className="grid" onSubmit={(event) => event.preventDefault()}>
          <label>WorkBuddy 版本<select value={build} onChange={(e) => setBuild(e.target.value)}><option value="workbuddy">中国版</option><option value="workbuddy-ai">国际版</option></select></label>
          <label>API Key<input type="password" value={apiKey} placeholder="ck_…" onChange={(e) => setApiKey(e.target.value)}/></label>
          <div className="form-actions align-start"><button type="button" className="secondary" onClick={() => submit({ build, apiKey }, "已保存这把 API Key。")}>用 API Key 登录</button></div>
          <label className="form-wide">导入登录 JSON<textarea value={blob} rows={4} placeholder="桌面登录文件，或带 access 与 refresh 的 JSON" onChange={(e) => setBlob(e.target.value)}/></label>
          <p className="form-note form-wide">导入会向 WorkBuddy 换一对新令牌。桌面文件本身不改，但原来的刷新令牌会失效。</p>
          <div className="form-actions align-start"><button type="button" className="secondary" onClick={() => submit({ build, import: blob }, "已导入这份登录。")}>导入 JSON</button><button type="button" className="ghost" onClick={() => submit({ build, desktop: true }, "已从桌面登录文件导入。")}>从桌面登录文件导入</button></div>
        </form>
      </div> : null}
    </div>
  </Panel>;
}
