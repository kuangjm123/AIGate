import { useEffect, useState } from "react";
import { api, asList, splitList } from "../api.js";
import { Panel, Table, report } from "../components/ui.jsx";
import { SubscriptionAccounts } from "./SubscriptionAccounts.jsx";
import { canonicalProtocol, idsWith, kindOf, modelInfo, probeText, providerProtocols } from "./providerUtils.js";

export function Providers({ tick, refresh, setMessage }) {
  const [list, setList] = useState([]);
  const [editing, setEditing] = useState(null);
  const [name, setName] = useState("");
  const [protocol, setProtocol] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [models, setModels] = useState("");
  const [hidden, setHidden] = useState("");
  const [textOnly, setTextOnly] = useState("");
  const [images, setImages] = useState("");
  const [videos, setVideos] = useState("");
  const [keys, setKeys] = useState([""]);
  const [savedKeys, setSavedKeys] = useState(0);
  const [checks, setChecks] = useState([]);
  const [busy, setBusy] = useState("");
  const [testing, setTesting] = useState("");
  const [accounts, setAccounts] = useState(null);

  useEffect(() => {
    api("GET", "/providers").then((body) => setList(asList(body))).catch((err) => report(setMessage, err));
    api("GET", "/accounts").then(setAccounts).catch((err) => report(setMessage, err));
  }, [tick]);

  function edit(row) {
    setEditing(row.id);
    setName(row.name || "");
    setProtocol(canonicalProtocol(row.protocol || ""));
    setBaseUrl(row.baseUrl || "");
    setModels((row.models || []).filter((id) => !kindOf(row, id)).join(", "));
    setHidden(idsWith(row, (info) => info.hidden).join(", "));
    setTextOnly(idsWith(row, (info) => info.vision === false).join(", "));
    setImages(idsWith(row, (info) => info.kind === "image").join(", "));
    setVideos(idsWith(row, (info) => info.kind === "video").join(", "));
    setKeys([""]);
    setSavedKeys(row.keyCount || 0);
    setChecks([]);
  }

  function clearForm() {
    setEditing(null);
    setName("");
    setProtocol("");
    setBaseUrl("");
    setModels("");
    setHidden("");
    setTextOnly("");
    setImages("");
    setVideos("");
    setKeys([""]);
    setSavedKeys(0);
    setChecks([]);
  }

  function toolBody() {
    return { id: editing || "", baseUrl: baseUrl.trim(), protocol: protocol.trim(), credentials: keys.map((key) => key.trim()).filter(Boolean), models: splitList(models) };
  }

  async function save(event) {
    event.preventDefault();
    const chat = splitList(models);
    const imageIds = splitList(images);
    const videoIds = splitList(videos);
    const fresh = keys.map((key) => key.trim()).filter(Boolean);
    const body = {
      name: name.trim(), protocol: protocol.trim(), baseUrl: baseUrl.trim(),
      models: [...new Set([...chat, ...imageIds, ...videoIds, ...splitList(hidden), ...splitList(textOnly)])],
      info: modelInfo(chat, splitList(hidden), splitList(textOnly), imageIds, videoIds),
    };
    try {
      if (editing) {
        if (fresh.length) body.addCredentials = fresh;
        await api("PUT", "/providers/" + editing, body);
      } else {
        body.credentials = fresh;
        await api("POST", "/providers", body);
      }
      clearForm();
      setMessage(null);
      refresh();
    } catch (err) { report(setMessage, err); }
  }

  async function runTool(action) {
    setBusy(action);
    if (action === "check") setChecks([]);
    try {
      const path = action === "detect" ? "detect" : action === "models" ? "models" : "check";
      const result = await api("POST", "/providers/" + path, toolBody());
      if (result.error) {
        setMessage({ kind: "error", text: probeText({ error: result.error }) });
        return;
      }
      if (action === "detect") {
        setProtocol(canonicalProtocol(result.protocol || ""));
        setMessage({ kind: "note", text: `检测为 ${result.protocol}。OpenAI 的模型目录会计成 Chat Completions，Responses 需要自己改选。` });
      } else if (action === "models") {
        setModels((result.models || []).join(", "));
        setMessage({ kind: "note", text: `获取到 ${(result.models || []).length} 个模型。` });
      } else {
        const rows = result.results || [];
        setChecks(rows);
        const ok = rows.filter((row) => row.ok).length;
        setMessage({ kind: ok === rows.length && rows.length ? "note" : "error", text: `检验了 ${rows.length} 个模型，${ok} 个可用。` });
      }
    } catch (err) { report(setMessage, err); }
    finally { setBusy(""); }
  }

  async function removeSavedKey(index) {
    try {
      await api("DELETE", "/providers/" + editing + "/keys/" + index);
      setSavedKeys((count) => Math.max(0, count - 1));
      setMessage({ kind: "note", text: "已删除一把密钥。" });
      refresh();
    } catch (err) { report(setMessage, err); }
  }

  async function test(id) {
    setTesting(id);
    try {
      const result = await api("POST", "/providers/" + id + "/test");
      setMessage({ kind: result.ok ? "note" : "error", text: probeText(result) });
    } catch (err) { report(setMessage, err); }
    finally { setTesting(""); }
  }

  const knownProtocol = providerProtocols.some((item) => item.value === protocol);
  const selected = providerProtocols.find((item) => item.value === protocol) || { name: protocol, example: "", detail: "已保存的协议不在列表中。" };
  return <div className="stack">
    <SubscriptionAccounts accounts={accounts} refresh={refresh} setMessage={setMessage}/>
    <Panel title={editing ? "编辑提供方" : "添加提供方"} description={editing ? `正在编辑 ${editing}，留空的新密钥不会改变已保存密钥` : "配置上游协议、地址、模型与访问密钥"} actions={editing ? <button type="button" className="ghost" onClick={clearForm}>取消编辑</button> : null}>
      <form className="grid provider-form" onSubmit={save}>
        <div className="form-section-title form-wide"><span>基础配置</span><small>新建时名称和至少一把密钥必填</small></div>
        <label>名称<input value={name} placeholder="例如 My Relay" onChange={(e) => setName(e.target.value)} required/></label>
        <label>协议<select value={protocol} onChange={(e) => setProtocol(e.target.value)}>{knownProtocol ? null : <option value={protocol}>{protocol}</option>}{providerProtocols.map((item) => <option key={item.value || "same"} value={item.value}>{item.name}</option>)}</select></label>
        <label className="form-wide">Base URL<input value={baseUrl} placeholder={selected.example || "上游地址"} onChange={(e) => setBaseUrl(e.target.value)}/><small className="field-hint">{selected.example ? <>示例 <code>{selected.example}</code>。</> : null}{selected.detail}</small></label>
        <div className="keys form-wide"><span className="field-label">密钥{editing ? `（已保存 ${savedKeys} 把，留空则保持）` : "（至少一把）"}</span>{editing ? Array.from({ length: savedKeys }, (_, index) => <div className="key-row" key={"saved-" + index}><input value={"已保存的密钥 " + (index + 1)} readOnly/><button type="button" className="danger" onClick={() => removeSavedKey(index)}>删除</button></div>) : null}{keys.map((key, index) => <div className="key-row" key={"new-" + index}><input type="password" value={key} placeholder="粘贴 API 密钥" onChange={(e) => setKeys(keys.map((item, i) => i === index ? e.target.value : item))}/>{keys.length > 1 ? <button type="button" className="ghost" onClick={() => setKeys(keys.filter((_, i) => i !== index))}>去掉</button> : null}</div>)}<button type="button" className="ghost fit-content" onClick={() => setKeys([...keys, ""])}>+ 再加一把密钥</button></div>
        <div className="form-section-title form-wide"><span>模型配置</span><small>多个模型名使用英文逗号分隔</small></div>
        <label className="form-wide">对话模型<input value={models} placeholder="例如 gpt-5.5, claude-sonnet-5" onChange={(e) => setModels(e.target.value)}/></label>
        <div className="row-actions form-wide tool-actions"><button type="button" className="secondary" disabled={busy !== ""} onClick={() => runTool("detect")}>{busy === "detect" ? "检测中" : "检测协议"}</button><button type="button" className="secondary" disabled={busy !== ""} onClick={() => runTool("models")}>{busy === "models" ? "获取中" : "获取模型"}</button><button type="button" className="secondary" disabled={busy !== ""} onClick={() => runTool("check")}>{busy === "check" ? "检验中" : "逐个检验"}</button></div>
        {checks.length ? <ul className="checks form-wide">{checks.map((row) => <li key={row.model || row.error} className={row.ok ? "ok" : "bad"}>{row.ok ? `${row.model}：可用 ${row.status}` : probeText(row)}</li>)}</ul> : null}
        <details className="advanced form-wide"><summary>高级模型规则</summary><div className="grid advanced-grid"><label>隐藏模型<input value={hidden} placeholder="不出现在目录里，也不能调用" onChange={(e) => setHidden(e.target.value)}/></label><label>不接受图片<input value={textOnly} placeholder="拒绝带图片或视频的请求" onChange={(e) => setTextOnly(e.target.value)}/></label><label>图片模型<input value={images} placeholder="不进入对话目录" onChange={(e) => setImages(e.target.value)}/></label><label>视频模型<input value={videos} placeholder="不进入对话目录" onChange={(e) => setVideos(e.target.value)}/></label></div></details>
        <div className="form-actions form-wide"><button type="submit">{editing ? "更新提供方" : "添加提供方"}</button>{editing ? <button type="button" className="ghost" onClick={clearForm}>取消</button> : null}</div>
      </form>
    </Panel>
    <Panel title="提供方列表" description={`${list.length} 个上游服务`} className="table-panel"><Table headers={["名称", "ID", "协议", "Base URL", "模型", "密钥", "操作"]} rows={list.map((provider) => [
      <strong className="cell-title">{provider.name || provider.id}</strong>, <code>{provider.id}</code>, provider.protocol || "跟随调用方", provider.baseUrl ? <code>{provider.baseUrl}</code> : "—", (provider.models || []).join(", ") || "—", `${provider.keyCount || 0} 把`,
      <span className="row-actions"><button type="button" className="secondary" disabled={testing !== ""} onClick={() => test(provider.id)}>{testing === provider.id ? "测试中" : "测试"}</button><button type="button" className="secondary" onClick={() => edit(provider)}>编辑</button><button type="button" className="danger" onClick={async () => { try { await api("DELETE", "/providers/" + provider.id); refresh(); } catch (err) { report(setMessage, err); } }}>删除</button></span>,
    ])} empty="还没有提供方"/></Panel>
  </div>;
}
