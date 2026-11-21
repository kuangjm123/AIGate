import { useState } from "react";
import { api } from "./api.js";
import { Brand, Icon, PageHeader } from "./components/ui.jsx";
import { Agents, Connect, Conversations, Listeners, Plugins, Providers, Routes, Tokens, Usage } from "./sections/index.js";

const groups = [
  { label: "工作台", items: ["agents"] },
  { label: "网关配置", items: ["connect", "listeners", "providers", "routes", "plugins", "tokens"] },
  { label: "可观测性", items: ["usage", "conversations"] },
];

const sections = {
  agents: { title: "Agent 接入", eyebrow: "WORKSPACE", description: "发现本机开发工具，并将模型请求安全地接入网关。", component: Agents },
  connect: { title: "手动接入", eyebrow: "CONFIGURATION", description: "获取应用接入所需的地址、密钥与模型信息。", component: Connect },
  listeners: { title: "监听地址", eyebrow: "CONFIGURATION", description: "管理网关与控制台监听地址，查看实际运行状态。", component: Listeners },
  providers: { title: "模型提供方", eyebrow: "CONFIGURATION", description: "集中管理上游服务、订阅账号、模型和访问密钥。", component: Providers },
  routes: { title: "模型路由", eyebrow: "CONFIGURATION", description: "定义模型匹配规则、提供方顺序与降级策略。", component: Routes },
  plugins: { title: "插件记录", eyebrow: "CONFIGURATION", description: "维护插件启用状态和执行顺序。", component: Plugins },
  tokens: { title: "网关令牌", eyebrow: "SECURITY", description: "创建和撤销访问凭证，并按需限制模型范围。", component: Tokens },
  usage: { title: "用量记录", eyebrow: "OBSERVABILITY", description: "筛选和检查每一次模型调用的结果与资源消耗。", component: Usage },
  conversations: { title: "对话存档", eyebrow: "OBSERVABILITY", description: "按客户端会话查看完整的模型请求与回复。", component: Conversations },
};

export function Console({ username, onLogout }) {
  const [section, setSection] = useState("agents");
  const [message, setMessage] = useState(null);
  const [tick, setTick] = useState(0);
  const current = sections[section];
  const PanelComponent = current.component;

  function refresh() { setTick((value) => value + 1); }
  function open(id) { setSection(id); setMessage(null); window.scrollTo({ top: 0, behavior: "smooth" }); }
  async function logout() {
    try { await api("POST", "/logout"); } catch { /* local session is cleared either way */ }
    onLogout();
  }

  return <div className="console-shell">
    <header className="console-topbar">
      <Brand/>
      <div className="topbar-context"><span className="status-dot"/>本地控制台</div>
      <div className="cred"><span className="user-avatar">{(username || "U").slice(0, 1).toUpperCase()}</span><span className="username">{username}</span><span className="cred-divider"/><button type="button" className="ghost logout-button" onClick={logout}><Icon name="logout" size={16}/>退出</button></div>
    </header>
    <div className="layout">
      <aside className="sidebar">
        <nav aria-label="控制台导航">{groups.map((group) => <div className="nav-group" key={group.label}><div className="nav-label">{group.label}</div>{group.items.map((id) => <button key={id} type="button" className={id === section ? "active" : ""} aria-current={id === section ? "page" : undefined} onClick={() => open(id)}><Icon name={id}/><span>{sections[id].title}</span>{id === section ? <Icon name="arrow" size={14} className="nav-arrow"/> : null}</button>)}</div>)}</nav>
        <div className="sidebar-footer"><span className="status-dot"/><div><strong>AIGate Gateway</strong><small>配置实时生效</small></div></div>
      </aside>
      <main className="content" id="main-content">
        <PageHeader eyebrow={current.eyebrow} title={current.title} description={current.description} meta={<button type="button" className="secondary refresh-button" onClick={refresh}>刷新数据</button>}/>
        {message ? <p className={`${message.kind} global-message`} role={message.kind === "error" ? "alert" : "status"}>{message.text}</p> : null}
        <PanelComponent tick={tick} refresh={refresh} setMessage={setMessage}/>
      </main>
    </div>
  </div>;
}
