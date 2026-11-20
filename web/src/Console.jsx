import { useState } from "react";
import { api } from "./api.js";
import { Agents, Connect, Conversations, Listeners, Plugins, Providers, Routes, Tokens, Usage } from "./sections.jsx";

const sections = [
  ["agents", "Agent"],
  ["connect", "接入"],
  ["listeners", "监听"],
  ["providers", "提供方"],
  ["routes", "路由"],
  ["plugins", "插件"],
  ["tokens", "网关令牌"],
  ["usage", "用量"],
  ["conversations", "对话"],
];

export function Console({ username, onLogout }) {
  const [section, setSection] = useState("agents");
  const [message, setMessage] = useState(null);
  const [tick, setTick] = useState(0);

  function refresh() {
    setTick((n) => n + 1);
  }

  function open(id) {
    setSection(id);
    setMessage(null);
  }

  async function logout() {
    try { await api("POST", "/logout"); } catch { /* the local session is cleared either way */ }
    onLogout();
  }

  const panel = {
    agents: <Agents tick={tick} setMessage={setMessage} />,
    connect: <Connect tick={tick} />,
    listeners: <Listeners tick={tick} refresh={refresh} setMessage={setMessage} />,
    providers: <Providers tick={tick} refresh={refresh} setMessage={setMessage} />,
    routes: <Routes tick={tick} refresh={refresh} setMessage={setMessage} />,
    plugins: <Plugins tick={tick} refresh={refresh} setMessage={setMessage} />,
    tokens: <Tokens tick={tick} refresh={refresh} setMessage={setMessage} />,
    usage: <Usage tick={tick} setMessage={setMessage} />,
    conversations: <Conversations tick={tick} setMessage={setMessage} />,
  }[section];

  return (
    <>
      <header>
        <h1>AIGate <span>控制台</span></h1>
        <div className="cred">
          <span className="status">{username}</span>
          <button type="button" className="ghost" onClick={logout}>退出</button>
        </div>
      </header>
      <div className="layout">
        <nav>
          {sections.map(([id, label]) => (
            <button key={id} type="button" className={id === section ? "active" : ""} onClick={() => open(id)}>{label}</button>
          ))}
        </nav>
        <main>
          {message ? <p className={message.kind}>{message.text}</p> : null}
          {panel}
        </main>
      </div>
    </>
  );
}
