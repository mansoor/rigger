import { useState, useRef, useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  fetchContainerFiles, fetchContainerFile, saveContainerFile,
  deleteContainerFile, uploadContainerFile, downloadContainerFile,
  renameContainerFile, chmodContainerFile, mkdirContainerDir, newContainerFile,
} from '../lib/api'

// FileBrowserModal — Wave D. Browse, view, edit, upload, download, rename,
// chmod, create and delete files inside a container, on whichever daemon it runs
// on (local or remote over SSH).

function humanSize(n) {
  if (n == null) return ''
  if (n < 1024) return `${n} B`
  const u = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024, i = 0
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${u[i]}`
}

const parentOf = (p) => {
  if (p === '/' || p === '') return '/'
  const i = p.lastIndexOf('/')
  return i <= 0 ? '/' : p.slice(0, i)
}
const joinPath = (dir, name) => (dir === '/' ? `/${name}` : `${dir}/${name}`)

// permsToOctal turns an `ls` mode string (e.g. -rwxr-xr-x) into its octal form
// (755), used to pre-fill the chmod dialog. Special bits are ignored.
function permsToOctal(perms) {
  if (!perms || perms.length < 10) return ''
  const tri = (s) => (s[0] !== '-' ? 4 : 0) + (s[1] !== '-' ? 2 : 0) + (s[2] !== '-' ? 1 : 0)
  return `${tri(perms.slice(1, 4))}${tri(perms.slice(4, 7))}${tri(perms.slice(7, 10))}`
}

// ── Icons (monochrome, currentColor — no OS color emoji) ──────────────────────
const sw = { fill: 'none', stroke: 'currentColor', strokeWidth: 1.6, strokeLinecap: 'round', strokeLinejoin: 'round' }
function EntryIcon({ type }) {
  const cls = 'w-4 h-4 shrink-0'
  if (type === 'dir') return (
    <svg viewBox="0 0 20 20" className={`${cls} text-warning-fg/80`} fill="currentColor" aria-hidden="true">
      <path d="M2 5a2 2 0 012-2h4l2 2h6a2 2 0 012 2v7a2 2 0 01-2 2H4a2 2 0 01-2-2V5z" />
    </svg>
  )
  if (type === 'link') return (
    <svg viewBox="0 0 20 20" className={`${cls} text-sky-400/80`} {...sw} aria-hidden="true">
      <path d="M8 12a3 3 0 004.2 0l2.3-2.3a3 3 0 00-4.2-4.2l-1 1" />
      <path d="M12 8a3 3 0 00-4.2 0L5.5 10.3a3 3 0 004.2 4.2l1-1" />
    </svg>
  )
  return (
    <svg viewBox="0 0 20 20" className={`${cls} text-content-subtle`} {...sw} aria-hidden="true">
      <path d="M5 3h6l4 4v10a1 1 0 01-1 1H5a1 1 0 01-1-1V4a1 1 0 011-1z" />
      <path d="M11 3v4h4" />
    </svg>
  )
}
const ai = 'w-3.5 h-3.5'
const EyeIcon = () => (
  <svg viewBox="0 0 20 20" className={ai} {...sw} aria-hidden="true"><path d="M1.5 10S4.5 4.5 10 4.5 18.5 10 18.5 10 15.5 15.5 10 15.5 1.5 10 1.5 10z" /><circle cx="10" cy="10" r="2.2" /></svg>
)
const DownloadIcon = () => (
  <svg viewBox="0 0 20 20" className={ai} {...sw} aria-hidden="true"><path d="M10 3v9m0 0l-3.2-3.2M10 12l3.2-3.2" /><path d="M4 15.5h12" /></svg>
)
const PencilIcon = () => (
  <svg viewBox="0 0 20 20" className={ai} {...sw} aria-hidden="true"><path d="M13.5 3.5l3 3L7 16l-3.5.5L4 13z" /></svg>
)
const ShieldLockIcon = () => (
  <svg viewBox="0 0 20 20" className={ai} {...sw} aria-hidden="true">
    <path d="M10 2.5l5.5 1.9v4.7c0 3.5-2.4 6-5.5 7.4-3.1-1.4-5.5-3.9-5.5-7.4V4.4z" />
    <rect x="7.8" y="9.4" width="4.4" height="3.4" rx="0.6" />
    <path d="M8.7 9.4V8.3a1.3 1.3 0 012.6 0v1.1" />
  </svg>
)
const TrashIcon = () => (
  <svg viewBox="0 0 20 20" className={ai} {...sw} aria-hidden="true"><path d="M4 6h12M8 6V4h4v2M6 6l1 10h6l1-10" /></svg>
)
const NewFileIcon = () => (
  <svg viewBox="0 0 20 20" className="w-4 h-4" {...sw} aria-hidden="true"><path d="M5 3h6l4 4v10a1 1 0 01-1 1H5a1 1 0 01-1-1V4a1 1 0 011-1z" /><path d="M11 3v4h4" /><path d="M10 10v4M8 12h4" /></svg>
)
const NewFolderIcon = () => (
  <svg viewBox="0 0 20 20" className="w-4 h-4" {...sw} aria-hidden="true"><path d="M2 5a2 2 0 012-2h4l2 2h6a2 2 0 012 2v7a2 2 0 01-2 2H4a2 2 0 01-2-2V5z" /><path d="M10 9v4M8 11h4" /></svg>
)
const UploadIcon = () => (
  <svg viewBox="0 0 20 20" className="w-4 h-4" {...sw} aria-hidden="true"><path d="M10 14V5m0 0L6.8 8.2M10 5l3.2 3.2" /><path d="M4 15.5h12" /></svg>
)

function ActBtn({ title, onClick, hover = 'hover:text-content', children }) {
  return (
    <button title={title} onClick={onClick}
      className={`p-1 rounded text-content-faint ${hover} hover:bg-surface-raised transition-colors`}>{children}</button>
  )
}
function ToolBtn({ title, onClick, disabled, children }) {
  return (
    <button title={title} onClick={onClick} disabled={disabled}
      className="p-1.5 rounded-lg bg-surface-raised hover:bg-surface-overlay text-content disabled:opacity-40 transition-colors">{children}</button>
  )
}

export default function FileBrowserModal({ workspace, wsName, env, service, short, onClose }) {
  const [cwd, setCwd]           = useState('/')
  const [view, setView]         = useState(null)   // { path, content, truncated, binary, size } | null
  const [editing, setEditing]   = useState(false)
  const [draft, setDraft]       = useState('')
  const [busy, setBusy]         = useState(false)
  const [error, setError]       = useState('')
  const [confirmDel, setConfirmDel] = useState(null) // entry pending delete
  const [prompt, setPrompt]     = useState(null)     // generic input dialog
  const fileInputRef = useRef(null)

  const { data, isLoading, isError, error: listErr, refetch } = useQuery({
    queryKey: ['cfiles', workspace, wsName, env, service, cwd],
    queryFn:  () => fetchContainerFiles(workspace, wsName, env, service, cwd),
    retry: false,
  })

  const entries = (data?.entries || [])
    .filter(e => e.name !== '.' && e.name !== '..')
    .sort((a, b) => (a.type === 'dir') === (b.type === 'dir')
      ? a.name.localeCompare(b.name)
      : a.type === 'dir' ? -1 : 1)

  // runAct wraps a mutation: busy/error handling + refetch on success.
  async function runAct(fn, after) {
    setBusy(true); setError('')
    try { await fn(); if (after) after(); refetch() }
    catch (err) { setError(errMsg(err)) }
    finally { setBusy(false) }
  }

  async function openEntry(e) {
    setError('')
    const target = joinPath(cwd, e.name)
    if (e.type === 'dir') { setView(null); setCwd(target); return }
    setBusy(true)
    try {
      const v = await fetchContainerFile(workspace, wsName, env, service, target)
      setView(v); setEditing(false); setDraft(v.content || '')
    } catch (err) { setError(errMsg(err)) } finally { setBusy(false) }
  }

  function onSave() {
    runAct(
      () => saveContainerFile(workspace, wsName, env, service, view.path, draft),
      () => { setView({ ...view, content: draft, size: draft.length, truncated: false }); setEditing(false) },
    )
  }
  function onDelete(e) {
    const target = joinPath(cwd, e.name)
    runAct(
      () => deleteContainerFile(workspace, wsName, env, service, target),
      () => { if (view?.path === target) setView(null); setConfirmDel(null) },
    )
  }
  function onDownload(e) {
    runAct(() => downloadContainerFile(workspace, wsName, env, service, joinPath(cwd, e.name)))
  }
  function onUpload(ev) {
    const file = ev.target.files?.[0]
    ev.target.value = ''
    if (!file) return
    runAct(() => uploadContainerFile(workspace, wsName, env, service, cwd, file))
  }

  // Prompt-driven mutations.
  const onNewFile = () => setPrompt({
    title: 'New file', label: 'File name', placeholder: 'example.txt', confirmLabel: 'Create',
    onSubmit: (n) => { setPrompt(null); runAct(() => newContainerFile(workspace, wsName, env, service, joinPath(cwd, n))) },
  })
  const onNewFolder = () => setPrompt({
    title: 'New folder', label: 'Folder name', placeholder: 'newdir', confirmLabel: 'Create',
    onSubmit: (n) => { setPrompt(null); runAct(() => mkdirContainerDir(workspace, wsName, env, service, joinPath(cwd, n))) },
  })
  const onRename = (e) => setPrompt({
    title: `Rename "${e.name}"`, label: 'New name', initial: e.name, confirmLabel: 'Rename',
    onSubmit: (n) => {
      setPrompt(null)
      const from = joinPath(cwd, e.name), to = joinPath(cwd, n)
      runAct(() => renameContainerFile(workspace, wsName, env, service, from, to), () => { if (view?.path === from) setView(null) })
    },
  })
  const onChmod = (e) => setPrompt({
    title: `Permissions for "${e.name}"`, label: 'Octal mode (e.g. 644)', initial: permsToOctal(e.mode),
    placeholder: '644', confirmLabel: 'Apply',
    onSubmit: (m) => { setPrompt(null); runAct(() => chmodContainerFile(workspace, wsName, env, service, joinPath(cwd, e.name), m)) },
  })

  const crumbs = cwd === '/' ? [] : cwd.split('/').filter(Boolean)
  const crumbPath = (i) => '/' + crumbs.slice(0, i + 1).join('/')

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" onClick={onClose}>
      <div
        className="relative bg-surface border border-border-strong rounded-xl flex flex-col shadow-2xl w-full max-w-4xl h-[600px] max-h-[88vh]"
        onClick={e => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center gap-3 px-4 py-3 border-b border-border shrink-0">
          <span className="text-sm font-medium text-content">Files</span>
          <span className="font-mono text-xs text-content-subtle truncate">{short || service}</span>
          <span className="text-xs text-content-faint">· {wsName}/{env}</span>
          <div className="ml-auto flex items-center gap-1.5">
            <ToolBtn title="New file" onClick={onNewFile} disabled={busy}><NewFileIcon /></ToolBtn>
            <ToolBtn title="New folder" onClick={onNewFolder} disabled={busy}><NewFolderIcon /></ToolBtn>
            <ToolBtn title="Upload file" onClick={() => fileInputRef.current?.click()} disabled={busy}><UploadIcon /></ToolBtn>
            <input ref={fileInputRef} type="file" className="hidden" onChange={onUpload} />
            <ToolBtn title="Refresh" onClick={() => refetch()} disabled={busy}><span className="text-sm leading-none">⟳</span></ToolBtn>
            <button onClick={onClose} className="ml-1 text-content-subtle hover:text-content-strong text-lg leading-none transition-colors" title="Close">×</button>
          </div>
        </div>

        {/* Breadcrumb */}
        <div className="flex items-center gap-1 px-4 py-2 border-b border-border text-xs shrink-0 overflow-x-auto">
          <button onClick={() => { setView(null); setCwd('/') }}
            className={`px-1.5 py-0.5 rounded hover:bg-surface-raised ${cwd === '/' ? 'text-content' : 'text-content-muted'}`}>/</button>
          {crumbs.map((c, i) => (
            <span key={i} className="flex items-center gap-1">
              <span className="text-content-faint">/</span>
              <button onClick={() => { setView(null); setCwd(crumbPath(i)) }}
                className={`px-1.5 py-0.5 rounded hover:bg-surface-raised ${i === crumbs.length - 1 ? 'text-content' : 'text-content-muted'}`}>{c}</button>
            </span>
          ))}
        </div>

        {error && (
          <div className="px-4 py-2 bg-danger-subtle/50 border-b border-danger-border/50 text-xs text-danger-fg shrink-0">{error}</div>
        )}

        {/* Body: file viewer/editor OR directory listing */}
        <div className="flex-1 min-h-0 overflow-y-auto">
          {view ? (
            <FileViewer
              view={view} editing={editing} draft={draft} setDraft={setDraft}
              onEdit={() => setEditing(true)} onCancel={() => { setEditing(false); setDraft(view.content || '') }}
              onSave={onSave} onBack={() => setView(null)} busy={busy}
              onDownloadPath={() => downloadContainerFile(workspace, wsName, env, service, view.path).catch(err => setError(errMsg(err)))}
            />
          ) : (
            <table className="w-full text-xs">
              <thead className="sticky top-0 z-[1] bg-surface">
                <tr className="text-content-subtle border-b border-border">
                  <th className="text-left font-medium py-1.5 pl-4 pr-2">Name</th>
                  <th className="text-right font-medium py-1.5 px-2">Size</th>
                  <th className="text-left font-medium py-1.5 px-2 hidden sm:table-cell">Permission</th>
                  <th className="text-left font-medium py-1.5 px-2 hidden sm:table-cell">Modified</th>
                  <th className="text-right font-medium py-1.5 pl-2 pr-4">Actions</th>
                </tr>
              </thead>
              <tbody>
                {cwd !== '/' && (
                  <tr className="border-b border-border/60 hover:bg-surface-raised/40 cursor-pointer"
                    onClick={() => { setView(null); setCwd(parentOf(cwd)) }}>
                    <td className="py-1.5 px-4 text-content-muted" colSpan={5}>↑ ..</td>
                  </tr>
                )}
                {isLoading && <tr><td className="py-6 px-4 text-content-subtle" colSpan={5}>Loading…</td></tr>}
                {isError && <tr><td className="py-6 px-4 text-danger-fg" colSpan={5}>{errMsg(listErr)}</td></tr>}
                {!isLoading && !isError && entries.length === 0 && (
                  <tr><td className="py-6 px-4 text-content-faint" colSpan={5}>Empty directory</td></tr>
                )}
                {entries.map(e => (
                  <tr key={e.name} className="border-b border-border/60 hover:bg-surface-raised/40">
                    <td className="py-1.5 pl-4 pr-2 cursor-pointer" onClick={() => openEntry(e)}>
                      <div className="flex items-center gap-2 min-w-0">
                        <EntryIcon type={e.type} />
                        <span className={`font-mono truncate ${e.type === 'dir' ? 'text-content' : 'text-content'}`}>{e.name}</span>
                        {e.link && <span className="text-content-faint truncate">→ {e.link}</span>}
                      </div>
                    </td>
                    <td className="py-1.5 px-2 text-right text-content-subtle whitespace-nowrap cursor-pointer" onClick={() => openEntry(e)}>
                      {e.type === 'file' ? humanSize(e.size) : ''}
                    </td>
                    <td className="py-1.5 px-2 text-content-faint font-mono whitespace-nowrap hidden sm:table-cell">{e.mode}</td>
                    <td className="py-1.5 px-2 text-content-faint font-mono whitespace-nowrap hidden sm:table-cell">{e.mtime}</td>
                    <td className="py-1.5 pl-2 pr-4">
                      <div className="flex items-center justify-end gap-0.5">
                        {e.type === 'file' && <ActBtn title="View" onClick={() => openEntry(e)}><EyeIcon /></ActBtn>}
                        {e.type === 'file' && <ActBtn title="Download" hover="hover:text-info-fg" onClick={() => onDownload(e)}><DownloadIcon /></ActBtn>}
                        <ActBtn title="Rename" onClick={() => onRename(e)}><PencilIcon /></ActBtn>
                        <ActBtn title="Change permissions" onClick={() => onChmod(e)}><ShieldLockIcon /></ActBtn>
                        <ActBtn title="Delete" hover="hover:text-danger-fg" onClick={() => setConfirmDel(e)}><TrashIcon /></ActBtn>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        {/* Delete confirm */}
        {confirmDel && (
          <div className="absolute inset-0 z-10 flex items-center justify-center bg-black/60" onClick={() => setConfirmDel(null)}>
            <div className="bg-surface border border-border-strong rounded-xl p-5 max-w-sm" onClick={e => e.stopPropagation()}>
              <p className="text-sm text-content mb-1">Delete {confirmDel.type === 'dir' ? 'directory' : 'file'}?</p>
              <p className="font-mono text-xs text-content-muted break-all mb-4">{joinPath(cwd, confirmDel.name)}</p>
              {confirmDel.type === 'dir' && (
                <p className="text-xs text-warning-fg/80 mb-4">This removes the directory and everything inside it.</p>
              )}
              <div className="flex justify-end gap-2">
                <button onClick={() => setConfirmDel(null)}
                  className="px-3 py-1.5 text-xs rounded-lg bg-surface-raised hover:bg-surface-overlay text-content">Cancel</button>
                <button onClick={() => onDelete(confirmDel)} disabled={busy}
                  className="px-3 py-1.5 text-xs rounded-lg bg-danger-subtle/80 hover:bg-danger/20 text-danger-fg disabled:opacity-40">Delete</button>
              </div>
            </div>
          </div>
        )}

        {/* Generic input prompt (new file/folder, rename, chmod) */}
        {prompt && <PromptDialog {...prompt} busy={busy} onCancel={() => setPrompt(null)} />}
      </div>
    </div>
  )
}

function PromptDialog({ title, label, initial, placeholder, confirmLabel = 'OK', onCancel, onSubmit, busy }) {
  const [val, setVal] = useState(initial || '')
  const ref = useRef(null)
  useEffect(() => { ref.current?.focus(); ref.current?.select() }, [])
  const submit = () => { const v = val.trim(); if (v) onSubmit(v) }
  return (
    <div className="absolute inset-0 z-10 flex items-center justify-center bg-black/60" onClick={onCancel}>
      <div className="bg-surface border border-border-strong rounded-xl p-5 w-80" onClick={e => e.stopPropagation()}>
        <p className="text-sm text-content mb-3">{title}</p>
        {label && <label className="block text-xs text-content-subtle mb-1">{label}</label>}
        <input ref={ref} value={val} placeholder={placeholder}
          onChange={e => setVal(e.target.value)}
          onKeyDown={e => { if (e.key === 'Enter') submit(); if (e.key === 'Escape') onCancel() }}
          className="w-full bg-canvas border border-border-strong rounded-lg px-2.5 py-1.5 text-sm text-content font-mono focus:outline-none focus:border-brand-500" />
        <div className="flex justify-end gap-2 mt-4">
          <button onClick={onCancel} className="px-3 py-1.5 text-xs rounded-lg bg-surface-raised hover:bg-surface-overlay text-content">Cancel</button>
          <button onClick={submit} disabled={busy}
            className="px-3 py-1.5 text-xs rounded-lg bg-brand-600 hover:bg-brand-500 text-white disabled:opacity-40">{confirmLabel}</button>
        </div>
      </div>
    </div>
  )
}

function FileViewer({ view, editing, draft, setDraft, onEdit, onCancel, onSave, onBack, onDownloadPath, busy }) {
  return (
    <div className="flex flex-col h-full">
      <div className="flex items-center gap-2 px-4 py-2 border-b border-border shrink-0">
        <button onClick={onBack} className="px-2 py-0.5 text-xs rounded bg-surface-raised hover:bg-surface-overlay text-content">← Back</button>
        <span className="font-mono text-xs text-content truncate">{view.path}</span>
        {view.truncated && <span className="text-xs text-warning-fg/80">· truncated</span>}
        <div className="ml-auto flex items-center gap-2">
          <button onClick={onDownloadPath} className="px-2 py-0.5 text-xs rounded bg-surface-raised hover:bg-surface-overlay text-content-muted">↓ Download</button>
          {!view.binary && !view.truncated && !editing && (
            <button onClick={onEdit} className="px-2 py-0.5 text-xs rounded bg-surface-raised hover:bg-surface-overlay text-content">Edit</button>
          )}
          {editing && (
            <>
              <button onClick={onCancel} className="px-2 py-0.5 text-xs rounded bg-surface-raised hover:bg-surface-overlay text-content-muted">Cancel</button>
              <button onClick={onSave} disabled={busy}
                className="px-2.5 py-0.5 text-xs rounded bg-brand-600 hover:bg-brand-500 text-white disabled:opacity-40">Save</button>
            </>
          )}
        </div>
      </div>
      <div className="flex-1 min-h-0 overflow-auto">
        {view.binary ? (
          <div className="p-6 text-sm text-content-subtle">
            Binary file ({humanSize(view.size)}) — use Download to retrieve it.
          </div>
        ) : editing ? (
          <textarea
            value={draft} onChange={e => setDraft(e.target.value)} spellCheck={false}
            className="w-full h-full min-h-[380px] bg-canvas text-content font-mono text-xs p-4 resize-none focus:outline-none"
          />
        ) : (
          <pre className="p-4 font-mono text-xs text-content whitespace-pre-wrap break-words">{view.content}</pre>
        )}
      </div>
    </div>
  )
}

function errMsg(err) {
  return err?.response?.data?.error || err?.message || 'Request failed'
}
