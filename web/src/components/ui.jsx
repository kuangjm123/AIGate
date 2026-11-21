const iconPaths = {
  agents: <><rect x="4" y="5" width="16" height="14" rx="3"/><path d="M9 2v3m6-3v3M8 11h.01M16 11h.01M8 15h8"/></>,
  connect: <><path d="M8 12h8m-4-4 4 4-4 4"/><path d="M5 7H3v10h2m14-10h2v10h-2"/></>,
  listeners: <><path d="M4 12a8 8 0 0 1 16 0M7 12a5 5 0 0 1 10 0"/><circle cx="12" cy="12" r="2"/><path d="M12 14v7"/></>,
  providers: <><rect x="3" y="4" width="18" height="6" rx="2"/><rect x="3" y="14" width="18" height="6" rx="2"/><path d="M7 7h.01M7 17h.01M11 7h6M11 17h6"/></>,
  routes: <><circle cx="6" cy="6" r="2"/><circle cx="18" cy="6" r="2"/><circle cx="12" cy="18" r="2"/><path d="M8 6h8M7 8l4 8m6-8-4 8"/></>,
  plugins: <><path d="M8 3v4m8-4v4M6 7h12v5a6 6 0 0 1-12 0V7Zm6 11v3"/></>,
  tokens: <><circle cx="8" cy="12" r="4"/><path d="M12 12h9m-3 0v3m-3-3v2"/></>,
  usage: <><path d="M4 20V10m6 10V4m6 16v-7m4 7H2"/></>,
  conversations: <><path d="M20 15a3 3 0 0 1-3 3H9l-5 3v-6a3 3 0 0 1-1-2V7a3 3 0 0 1 3-3h11a3 3 0 0 1 3 3v8Z"/><path d="M8 9h8m-8 4h5"/></>,
  logout: <><path d="M10 4H5a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2h5m5-4 4-4-4-4m4 4H9"/></>,
  arrow: <><path d="m9 5 7 7-7 7"/></>,
  check: <path d="m5 12 4 4L19 6"/>,
  info: <><circle cx="12" cy="12" r="9"/><path d="M12 11v6m0-10h.01"/></>,
  empty: <><path d="M4 7h16v13H4zM8 7V4h8v3"/><path d="M9 12h6"/></>,
};

export function Icon({ name, size = 18, className = "" }) {
  return <svg className={`icon ${className}`} width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.7" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">{iconPaths[name] || iconPaths.info}</svg>;
}

export function Brand() {
  return <div className="brand"><span className="brand-mark"><span>A</span></span><span className="brand-copy"><strong>AIGate</strong><small>CONTROL CENTER</small></span></div>;
}

export function PageHeader({ eyebrow, title, description, meta }) {
  return <div className="page-heading"><div><span className="eyebrow">{eyebrow}</span><h2>{title}</h2><p>{description}</p></div>{meta ? <div className="page-meta">{meta}</div> : null}</div>;
}

export function Panel({ title, description, actions, children, className = "" }) {
  return <section className={`panel ${className}`}><div className="panel-heading"><div>{title ? <h3>{title}</h3> : null}{description ? <p>{description}</p> : null}</div>{actions ? <div className="panel-actions">{actions}</div> : null}</div><div className="panel-body">{children}</div></section>;
}

export function Table({ headers, rows, empty = "暂无数据" }) {
  return <div className="table-wrap"><table><thead><tr>{headers.map((header, index) => <th key={`${header}-${index}`}>{header}</th>)}</tr></thead><tbody>{rows.length ? rows.map((cells, rowIndex) => <tr key={rowIndex}>{cells.map((cell, cellIndex) => <td key={cellIndex}>{cell}</td>)}</tr>) : <tr><td colSpan={headers.length}><div className="empty-state"><Icon name="empty" size={22}/><span>{empty}</span></div></td></tr>}</tbody></table></div>;
}

export function Toggle({ label, hint, ...props }) {
  return <label className="toggle-field"><span className="toggle-copy"><strong>{label}</strong>{hint ? <small>{hint}</small> : null}</span><span className="switch"><input type="checkbox" {...props}/><span className="switch-track"/></span></label>;
}

export function report(setMessage, err) {
  setMessage({ kind: "error", text: err.status === 401 ? "请先登录" : err.message });
}
