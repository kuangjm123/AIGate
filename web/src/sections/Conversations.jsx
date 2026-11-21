import { useEffect, useState } from "react";
import { api } from "../api.js";
import { Panel, Table, report } from "../components/ui.jsx";

export function Conversations({ tick, setMessage }) {
  const [list, setList] = useState(null);
  const [session, setSession] = useState("");
  const [turns, setTurns] = useState(null);

  useEffect(() => { api("GET", "/conversations").then(setList).catch((err) => report(setMessage, err)); }, [tick]);

  async function open(id) {
    setSession(id);
    try { setTurns(await api("GET", "/conversations/turns?session=" + encodeURIComponent(id))); }
    catch (err) { report(setMessage, err); }
  }

  const conversations = list && list.status !== "unknown" ? (list.conversations || []) : [];
  const rows = turns && turns.status !== "unknown" ? (turns.turns || []) : [];
  return <div className="stack">
    <p className="section-intro">同一客户端会话的请求会归在一起。流式回复会先合并为完整文本，再记录为输出。</p>
    {list && list.status === "unknown" ? <p className="note standalone">对话存档不可读，不能当成没有对话。</p> : <Panel title="最近对话" description={`${conversations.length} 个会话`}><Table headers={["最近活动", "会话", "轮次", "操作"]} rows={conversations.map((item) => [item.last, <code>{item.session}</code>, `${item.turns} 轮`, <button type="button" className={session === item.session ? "" : "secondary"} onClick={() => open(item.session)}>{session === item.session ? "查看中" : "查看对话"}</button>])} empty="还没有对话记录"/></Panel>}
    {session && turns && turns.status === "unknown" ? <p className="note standalone">这段对话不可读。</p> : null}
    {session && rows.length ? <section className="conversation-detail"><div className="detail-heading"><div><span className="eyebrow">CONVERSATION</span><h3>{session}</h3></div><span className="pill">{rows.length} 轮</span></div>{rows.map((turn, index) => <article key={session + index} className="turn"><header><span className="turn-index">{String(index + 1).padStart(2, "0")}</span><span>{turn.at}</span><span>{turn.model || "未知模型"}</span><span>{turn.caller || ""}</span></header><div className="turn-body">{turn.input ? <div className="message-block input"><strong>输入</strong><p className="turn-text">{turn.input}</p></div> : null}<div className="message-block output"><strong>输出</strong><p className="turn-text">{turn.output}</p></div></div></article>)}</section> : null}
  </div>;
}
