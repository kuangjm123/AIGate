import { useEffect, useState } from "react";
import { api, asList, splitList } from "../api.js";
import { Panel, Table, report } from "../components/ui.jsx";
import { SubscriptionAccounts } from "./SubscriptionAccounts.jsx";
import { canonicalProtocol, idsWith, kindOf, modelInfo, probeText, providerProtocols } from "./providerUtils.js";

function defaultMultiplier(multiplier) {
  const value = Number(multiplier);
  return multiplier.trim() && Number.isFinite(value) && value >= 0 ? value : 1;
}

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
  const [multiplier, setMultiplier] = useState("");
  const [keyMultipliers, setKeyMultipliers] = useState("");
  const [maxConcurrent, setMaxConcurrent] = useState("");
  const [maxRPM, setMaxRPM] = useState("");
  const [queueSlots, setQueueSlots] = useState("");
  const [queueWaitMS, setQueueWaitMS] = useState("");
  const [balanceUrl, setBalanceUrl] = useState("");
  const [balanceField, setBalanceField] = useState("");
  const [balanceManual, setBalanceManual] = useState("");
  const [balanceCurrency, setBalanceCurrency] = useState("");
  const [checks, setChecks] = useState([]);
  const [busy, setBusy] = useState("");
  const [testing, setTesting] = useState("");
  const [accounts, setAccounts] = useState(null);
  const [seats, setSeats] = useState([]);

  useEffect(() => {
    api("GET", "/providers").then((body) => setList(asList(body))).catch((err) => report(setMessage, err));
    api("GET", "/accounts").then(setAccounts).catch((err) => report(setMessage, err));
    api("GET", "/seats").then((body) => setSeats(asList(body))).catch((err) => report(setMessage, err));
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
    setMultiplier(row.multiplier == null ? "" : String(row.multiplier));
    setKeyMultipliers((row.keyMultipliers || []).map((value) => String(value)).join(", "));
    setMaxConcurrent(row.maxConcurrent ? String(row.maxConcurrent) : "");
    setMaxRPM(row.maxRPM ? String(row.maxRPM) : "");
    setQueueSlots(row.queueSlots ? String(row.queueSlots) : "");
    setQueueWaitMS(row.queueWaitMS ? String(row.queueWaitMS) : "");
    setBalanceUrl(row.balanceUrl || "");
    setBalanceField(row.balanceField || "");
    setBalanceManual(row.balanceManual || "");
    setBalanceCurrency(row.balanceCurrency || "");
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
    setMultiplier("");
    setKeyMultipliers("");
    setMaxConcurrent("");
    setMaxRPM("");
    setQueueSlots("");
    setQueueWaitMS("");
    setBalanceUrl("");
    setBalanceField("");
    setBalanceManual("");
    setBalanceCurrency("");
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
    if (multiplier.trim()) {
      const value = Number(multiplier);
      if (!Number.isFinite(value) || value < 0) {
        setMessage({ kind: "error", text: "提供方倍率要写成非负数字" });
        return;
      }
      body.multiplier = value;
    }
    let perKey = splitList(keyMultipliers).map((value) => Number(value));
    if (perKey.some((value) => !Number.isFinite(value)) || perKey.some((value) => value < 0)) {
      setMessage({ kind: "error", text: "密钥倍率要写成非负数字，逗号分隔" });
      return;
    }
    const keyCount = (editing ? savedKeys : 0) + fresh.length;
    if (perKey.length) {
      const fill = defaultMultiplier(multiplier);
      if (perKey.length < keyCount) {
        perKey = [...perKey, ...Array(keyCount - perKey.length).fill(fill)];
      } else if (perKey.length > keyCount) {
        perKey = perKey.slice(0, keyCount);
      }
    }
    body.keyMultipliers = perKey;
    const limits = { maxConcurrent, maxRPM, queueSlots, queueWaitMS };
    for (const [key, text] of Object.entries(limits)) {
      if (!text.trim()) {
        body[key] = 0;
        continue;
      }
      const value = Number(text);
      if (!Number.isInteger(value) || value < 0) {
        setMessage({ kind: "error", text: "并发、RPM 和队列限制要写成非负整数" });
        return;
      }
      body[key] = value;
    }
    body.balanceUrl = balanceUrl.trim();
    body.balanceField = balanceField.trim();
    body.balanceManual = balanceManual.trim();
    body.balanceCurrency = balanceCurrency.trim();
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
      setKeyMultipliers((current) => {
        const values = splitList(current);
        if (values.length) values.splice(index, 1);
        return values.join(", ");
      });
      setMessage({ kind: "note", text: "已删除一把密钥。" });
      refresh();
    } catch (err) { report(setMessage, err); }
  }

  function addKey() {
    setKeys([...keys, ""]);
    setKeyMultipliers((current) => {
      if (!current.trim()) return current;
      return [...splitList(current), String(defaultMultiplier(multiplier))].join(", ");
    });
  }

  function removeNewKey(index) {
    setKeys(keys.filter((_, i) => i !== index));
    setKeyMultipliers((current) => {
      const values = splitList(current);
      const at = savedKeys + index;
      if (values.length && at < values.length) values.splice(at, 1);
      return values.join(", ");
    });
  }

  async function test(id) {
    setTesting(id);
    try {
      const result = await api("POST", "/providers/" + id + "/test");
      setMessage({ kind: result.ok ? "note" : "error", text: probeText(result) });
    } catch (err) { report(setMessage, err); }
    finally { setTesting(""); }
  }

  async function clearSeat(seat) {
    try {
      await api("POST", "/seats/reset", { seat });
      setMessage({ kind: "note", text: "已解除该席位的额度用尽标记，下一次请求会重新尝试它。" });
      refresh();
    } catch (err) { report(setMessage, err); }
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
        <div className="keys form-wide"><span className="field-label">密钥{editing ? `（已保存 ${savedKeys} 把，留空则保持）` : "（至少一把）"}</span>{editing ? Array.from({ length: savedKeys }, (_, index) => <div className="key-row" key={"saved-" + index}><input value={"已保存的密钥 " + (index + 1)} readOnly/><button type="button" className="danger" onClick={() => removeSavedKey(index)}>删除</button></div>) : null}{keys.map((key, index) => <div className="key-row" key={"new-" + index}><input type="password" value={key} placeholder="粘贴 API 密钥" onChange={(e) => setKeys(keys.map((item, i) => i === index ? e.target.value : item))}/>{keys.length > 1 ? <button type="button" className="ghost" onClick={() => removeNewKey(index)}>去掉</button> : null}</div>)}<button type="button" className="ghost fit-content" onClick={addKey}>+ 再加一把密钥</button></div>
        <div className="form-section-title form-wide"><span>模型配置</span><small>多个模型名使用英文逗号分隔</small></div>
        <label className="form-wide">对话模型<input value={models} placeholder="例如 gpt-5.5, claude-sonnet-5" onChange={(e) => setModels(e.target.value)}/></label>
        <div className="row-actions form-wide tool-actions"><button type="button" className="secondary" disabled={busy !== ""} onClick={() => runTool("detect")}>{busy === "detect" ? "检测中" : "检测协议"}</button><button type="button" className="secondary" disabled={busy !== ""} onClick={() => runTool("models")}>{busy === "models" ? "获取中" : "获取模型"}</button><button type="button" className="secondary" disabled={busy !== ""} onClick={() => runTool("check")}>{busy === "check" ? "检验中" : "逐个检验"}</button></div>
        {checks.length ? <ul className="checks form-wide">{checks.map((row) => <li key={row.model || row.error} className={row.ok ? "ok" : "bad"}>{row.ok ? `${row.model}：可用 ${row.status}` : probeText(row)}</li>)}</ul> : null}
        <details className="advanced form-wide"><summary>高级模型规则</summary><div className="grid advanced-grid"><label>隐藏模型<input value={hidden} placeholder="不出现在目录里，也不能调用" onChange={(e) => setHidden(e.target.value)}/></label><label>不接受图片<input value={textOnly} placeholder="拒绝带图片或视频的请求" onChange={(e) => setTextOnly(e.target.value)}/></label><label>图片模型<input value={images} placeholder="不进入对话目录" onChange={(e) => setImages(e.target.value)}/></label><label>视频模型<input value={videos} placeholder="不进入对话目录" onChange={(e) => setVideos(e.target.value)}/></label></div></details>
        <details className="advanced form-wide"><summary>计价倍率</summary><div className="grid advanced-grid"><label>提供方倍率<input value={multiplier} placeholder="留空或 1 表示目录价；0 表示免费" inputMode="decimal" onChange={(e) => setMultiplier(e.target.value)}/><small className="field-hint">乘在 models.dev 目录价上。用户为该供应商/模型写的价不再乘这层。</small></label><label>密钥倍率<input value={keyMultipliers} placeholder="按密钥顺序，逗号分隔；留空或 1 表示目录价" inputMode="decimal" onChange={(e) => setKeyMultipliers(e.target.value)}/><small className="field-hint">条数要与密钥数量一致。某把密钥自己的倍率优先于提供方倍率。</small></label></div></details>
        <details className="advanced form-wide"><summary>并发与限速</summary><div className="grid advanced-grid"><label>并发上限<input value={maxConcurrent} placeholder="每把密钥同时进行的请求数；留空不限" inputMode="numeric" onChange={(e) => setMaxConcurrent(e.target.value)}/><small className="field-hint">0 或留空表示不排队。每个密钥席位单独计数。</small></label><label>每分钟请求<input value={maxRPM} placeholder="每把密钥每分钟的请求起点；留空不限" inputMode="numeric" onChange={(e) => setMaxRPM(e.target.value)}/></label><label>队列长度<input value={queueSlots} placeholder="满员时最多几个请求等待；留空用默认" inputMode="numeric" onChange={(e) => setQueueSlots(e.target.value)}/></label><label>等待毫秒<input value={queueWaitMS} placeholder="超过这个等待时间返回 429；留空用默认" inputMode="numeric" onChange={(e) => setQueueWaitMS(e.target.value)}/></label></div></details>
        <details className="advanced form-wide"><summary>余额卡片</summary><div className="grid advanced-grid"><label>余额 URL<input value={balanceUrl} placeholder="可留空；内置 DeepSeek / OpenRouter" onChange={(e) => setBalanceUrl(e.target.value)}/><small className="field-hint">用户填写的 GET 地址，必须 http 或 https。查询失败会保留上一次成功读数。</small></label><label>余额字段<input value={balanceField} placeholder="例如 data.balance" onChange={(e) => setBalanceField(e.target.value)}/><small className="field-hint">响应里金额的点分 JSON 路径。留空时自动找常见字段。</small></label><label>手填余额<input value={balanceManual} placeholder="没有接口时显示这个数" onChange={(e) => setBalanceManual(e.target.value)}/><small className="field-hint">只在没有可用接口时显示，不会伪装成厂商读数。</small></label><label>币种<input value={balanceCurrency} placeholder="例如 USD 或 ¥" onChange={(e) => setBalanceCurrency(e.target.value)}/><small className="field-hint">加在手填余额前面。</small></label></div></details>
        <div className="form-actions form-wide"><button type="submit">{editing ? "更新提供方" : "添加提供方"}</button>{editing ? <button type="button" className="ghost" onClick={clearForm}>取消</button> : null}</div>
      </form>
    </Panel>
    <Panel title="提供方列表" description={`${list.length} 个上游服务`} className="table-panel"><Table headers={["名称", "ID", "协议", "Base URL", "模型", "密钥", "倍率", "操作"]} rows={list.map((provider) => [
      <strong className="cell-title">{provider.name || provider.id}</strong>, <code>{provider.id}</code>, provider.protocol || "跟随调用方", provider.baseUrl ? <code>{provider.baseUrl}</code> : "—", (provider.models || []).join(", ") || "—", `${provider.keyCount || 0} 把`, provider.multiplier == null ? "1" : provider.multiplier,
      <span className="row-actions"><button type="button" className="secondary" disabled={testing !== ""} onClick={() => test(provider.id)}>{testing === provider.id ? "测试中" : "测试"}</button><button type="button" className="secondary" onClick={() => edit(provider)}>编辑</button><button type="button" className="danger" onClick={async () => { try { await api("DELETE", "/providers/" + provider.id); refresh(); } catch (err) { report(setMessage, err); } }}>删除</button></span>,
    ])} empty="还没有提供方"/></Panel>
    <Panel title="席位" description="每把密钥、每个订阅账号各是一个席位；解除只清掉额度用尽和用尽再换的位置，不改变休息时间" className="table-panel"><Table headers={["席位", "类型", "所属", "操作"]} rows={seats.map((seat) => [
      <code>{seat.seat}</code>,
      seat.kind === "login" ? "订阅账号" : "密钥",
      seat.kind === "login" ? (seat.accountName || seat.account) : `${seat.providerName || seat.providerId}（第 ${(seat.keyIndex ?? 0) + 1} 把，${seat.keyFingerprint}）`,
      <button type="button" className="secondary" onClick={() => clearSeat(seat.seat)}>解除用尽</button>,
    ])} empty="还没有可用席位"/></Panel>
  </div>;
}
