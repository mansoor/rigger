import { useState, useEffect, useRef } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  fetchProjectNotes, fetchProjectNote, createProjectNote, saveProjectNote,
  deleteProjectNote, renderProjectNotes,
} from '../lib/api'
import { Hint, Btn } from './ui'

// Project Wiki / Notes tab. Multiple NAMED Markdown docs (one file each in the
// project's notes/ dir); each note's name is a sub-tab. Markdown is rendered +
// sanitized server-side, so the frontend ships NO markdown library. Edit is a
// plain textarea with a live side-by-side preview (debounced render call).

// Theme-agnostic styling for server-rendered markdown (translucent greys work in
// light + dark). Restores list markers the app's global reset strips.
const MD_CSS = `
.md-body { color: inherit; font-size: 0.9rem; line-height: 1.65; word-wrap: break-word; }
.md-body > *:first-child { margin-top: 0; }
.md-body h1,.md-body h2,.md-body h3,.md-body h4 { font-weight: 600; line-height: 1.3; margin: 1.4em 0 0.5em; }
.md-body h1 { font-size: 1.5em; border-bottom: 1px solid rgba(127,127,127,.25); padding-bottom: .25em; }
.md-body h2 { font-size: 1.3em; border-bottom: 1px solid rgba(127,127,127,.2); padding-bottom: .2em; }
.md-body h3 { font-size: 1.12em; }
.md-body p,.md-body ul,.md-body ol,.md-body blockquote,.md-body table,.md-body pre { margin: 0.6em 0; }
.md-body ul,.md-body ol { padding-left: 1.6em; }
.md-body ul { list-style: disc outside; }
.md-body ul ul { list-style: circle outside; }
.md-body ol { list-style: decimal outside; }
.md-body li { margin: 0.2em 0; display: list-item; }
.md-body li::marker { color: inherit; }
.md-body li > ul,.md-body li > ol { margin: 0.2em 0; }
.md-body a { color: #3b82f6; text-decoration: underline; }
.md-body code { background: rgba(127,127,127,.16); padding: .12em .35em; border-radius: 4px; font-size: .88em; font-family: ui-monospace,SFMono-Regular,Menlo,monospace; }
.md-body pre { background: rgba(127,127,127,.12); border: 1px solid rgba(127,127,127,.22); border-radius: 8px; padding: .8em 1em; overflow-x: auto; }
.md-body pre code { background: none; padding: 0; font-size: .85em; }
.md-body blockquote { border-left: 3px solid rgba(127,127,127,.4); padding-left: 1em; opacity: .85; }
.md-body table { border-collapse: collapse; display: block; overflow-x: auto; }
.md-body th,.md-body td { border: 1px solid rgba(127,127,127,.3); padding: .4em .7em; }
.md-body th { background: rgba(127,127,127,.12); font-weight: 600; }
.md-body hr { border: 0; border-top: 1px solid rgba(127,127,127,.3); margin: 1.2em 0; }
.md-body img { max-width: 100%; height: auto; border-radius: 6px; }
`

export default function ProjectNotesTab({ workspace, name }) {
  const qc = useQueryClient()
  const notesQ = useQuery({
    queryKey: ['project-notes', workspace, name],
    queryFn: () => fetchProjectNotes(workspace, name),
  })
  const notes = notesQ.data?.notes || []

  const [activeId, setActiveId] = useState(null)
  const [adding, setAdding] = useState(false)
  const [newName, setNewName] = useState('')
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState('')
  const [draftName, setDraftName] = useState('')
  const [previewHtml, setPreviewHtml] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const debounceRef = useRef(null)

  // Keep a valid note selected as the list changes.
  useEffect(() => {
    if (notes.length === 0) { if (activeId !== null) setActiveId(null); return }
    if (!activeId || !notes.some(n => n.id === activeId)) setActiveId(notes[0].id)
  }, [notes, activeId])

  const noteQ = useQuery({
    queryKey: ['project-note', workspace, name, activeId],
    queryFn: () => fetchProjectNote(workspace, name, activeId),
    enabled: !!activeId && !editing,
  })
  const note = noteQ.data

  // Debounced server render for the live preview while editing.
  useEffect(() => {
    if (!editing) return
    if (debounceRef.current) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(async () => {
      try { setPreviewHtml((await renderProjectNotes(workspace, name, draft)).html || '') } catch { /* keep last */ }
    }, 350)
    return () => clearTimeout(debounceRef.current)
  }, [draft, editing, workspace, name])

  function switchTo(id) { setEditing(false); setError(''); setActiveId(id) }

  function startEdit() {
    setDraft(note?.content || '')
    setDraftName(note?.name || '')
    setPreviewHtml(note?.html || '')
    setError(''); setEditing(true)
  }

  async function addNote() {
    const nm = newName.trim()
    if (!nm) return
    setBusy(true); setError('')
    try {
      const created = await createProjectNote(workspace, name, nm)
      setAdding(false); setNewName('')
      await notesQ.refetch()
      setActiveId(created.id)
      // Jump straight into editing the new (empty) note.
      setDraft(''); setDraftName(created.name); setPreviewHtml(''); setEditing(true)
    } catch (e) {
      setError(e?.response?.data?.error || 'Failed to add note')
    } finally { setBusy(false) }
  }

  async function save() {
    if (!draftName.trim()) { setError('A note name is required'); return }
    setBusy(true); setError('')
    try {
      const res = await saveProjectNote(workspace, name, activeId, draft, draftName.trim())
      qc.setQueryData(['project-note', workspace, name, activeId], res)
      await notesQ.refetch()
      setEditing(false)
    } catch (e) {
      setError(e?.response?.data?.error || 'Failed to save note')
    } finally { setBusy(false) }
  }

  async function remove() {
    if (!window.confirm(`Delete the note “${note?.name || draftName}”? This can't be undone.`)) return
    setBusy(true); setError('')
    try {
      await deleteProjectNote(workspace, name, activeId)
      setEditing(false)
      const remaining = notes.filter(n => n.id !== activeId)
      await notesQ.refetch()
      setActiveId(remaining[0]?.id || null)
    } catch (e) {
      setError(e?.response?.data?.error || 'Failed to delete note')
    } finally { setBusy(false) }
  }

  return (
    <section className="mb-6">
      <style>{MD_CSS}</style>
      <div className="mb-3">
        <h2 className="text-sm font-semibold text-content">Notes</h2>
        <Hint className="text-xs mt-0.5">Project wiki — multiple named Markdown docs, stored in the project’s <code className="font-mono">notes/</code> folder and included in backups.</Hint>
      </div>

      {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2 mb-3">{error}</p>}

      {/* Toolbar row — in edit mode it holds the name field + actions; in view mode
          the scrollable note tabs with a pinned (always-visible) Edit button. */}
      {editing ? (
        <div className="flex items-center flex-wrap gap-2 border-b border-border mb-4 min-h-[2.75rem] py-1">
          <label htmlFor="note-name" className="text-[11px] font-semibold uppercase tracking-wider text-content-muted shrink-0">Note name</label>
          <input id="note-name" value={draftName} onChange={e => setDraftName(e.target.value)} placeholder="Note name"
            className="w-48 px-3 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500 shrink-0" />
          <div className="flex items-center gap-2 ml-auto shrink-0">
            <Btn variant="dangerSubtle" size="sm" onClick={remove} disabled={busy} >Delete</Btn>
            <Btn variant="secondary" size="sm" onClick={() => { setEditing(false); setError('') }} disabled={busy}
              >Cancel</Btn>
            <Btn variant="primary" size="sm" onClick={save} disabled={busy} >{busy ? 'Saving…' : 'Save'}</Btn>
          </div>
        </div>
      ) : (
        <div className="flex items-end gap-2 border-b border-border mb-4 min-h-[2.75rem]">
          <div className="flex items-end gap-1 overflow-x-auto overflow-y-hidden flex-1 min-w-0">
            {notes.map(n => (
              <button key={n.id} onClick={() => switchTo(n.id)}
                className={`shrink-0 px-3 py-2.5 text-sm font-medium border-b-2 -mb-px transition-colors ${n.id === activeId ? 'border-brand-500 text-content-strong' : 'border-transparent text-content-subtle hover:text-content-strong'}`}>
                {n.name}
              </button>
            ))}
            {adding ? (
              <div className="flex items-center gap-1 px-2 py-1 shrink-0">
                <input value={newName} onChange={e => setNewName(e.target.value)} autoFocus
                  onKeyDown={e => { if (e.key === 'Enter') addNote(); if (e.key === 'Escape') { setAdding(false); setNewName('') } }}
                  placeholder="Note name" disabled={busy}
                  className="w-36 px-2 py-1 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500" />
                <Btn variant="primary" size="xs" onClick={addNote} disabled={busy || !newName.trim()} >Add</Btn>
                <button onClick={() => { setAdding(false); setNewName('') }} className="px-1.5 text-content-subtle hover:text-content-strong text-sm">×</button>
              </div>
            ) : (
              <Btn variant="ghost" size="md" onClick={() => setAdding(true)} title="Add a note"
                className="shrink-0">＋ Add</Btn>
            )}
          </div>
          {notes.length > 0 && (
            <Btn variant="primary" size="sm" onClick={startEdit} disabled={!note} className="shrink-0 mb-1.5">Edit</Btn>
          )}
        </div>
      )}

      {/* Body */}
      {notesQ.isLoading ? (
        <p className="text-sm text-content-subtle py-8 text-center">Loading…</p>
      ) : editing ? (
        <div className="space-y-3">
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-3">
            <div>
              <p className="text-[11px] font-semibold uppercase tracking-wider text-content-muted mb-1">Markdown</p>
              <textarea
                value={draft} onChange={e => setDraft(e.target.value)} spellCheck={false}
                placeholder={'# Heading\n\n- bullet\n1. step\n\n`code`, **bold**, tables…'}
                className="w-full h-[58vh] bg-canvas border border-border-strong rounded-lg px-3 py-2 text-sm font-mono text-content focus:border-brand-500 focus:outline-none resize-none" />
            </div>
            <div>
              <p className="text-[11px] font-semibold uppercase tracking-wider text-content-muted mb-1">Preview</p>
              <div className="h-[58vh] overflow-y-auto bg-surface border border-border rounded-lg p-4">
                {previewHtml
                  ? <div className="md-body" dangerouslySetInnerHTML={{ __html: previewHtml }} />
                  : <p className="text-sm text-content-faint">Nothing to preview yet.</p>}
              </div>
            </div>
          </div>
        </div>
      ) : notes.length === 0 ? (
        <div className="bg-surface border border-border rounded-xl p-10 text-center">
          <p className="text-3xl mb-2">📝</p>
          <p className="text-sm text-content-subtle">No notes yet. Add a project wiki, runbook, or any documentation in Markdown.</p>
          <Btn variant="primary" size="sm" onClick={() => setAdding(true)}
            className="mt-4">Add your first note</Btn>
        </div>
      ) : noteQ.isLoading ? (
        <p className="text-sm text-content-subtle py-8 text-center">Loading…</p>
      ) : note?.html ? (
        <div className="bg-surface border border-border rounded-xl p-5">
          <div className="md-body" dangerouslySetInnerHTML={{ __html: note.html }} />
        </div>
      ) : (
        <div className="bg-surface border border-border rounded-xl p-10 text-center">
          <p className="text-sm text-content-subtle">“{note?.name}” is empty. Click <strong className="text-content">Edit</strong> to add content.</p>
        </div>
      )}
    </section>
  )
}
