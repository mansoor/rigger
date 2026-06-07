import { createContext, useCallback, useContext, useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fetchGeneralSettings } from '../lib/api'
import { useAuthStore } from '../store/auth'

// confirm(opts) → Promise<boolean>. opts: { title, message, confirmLabel,
// cancelLabel, danger }. When the global "Confirm destructive actions" setting
// is off, it resolves true immediately (no dialog shown).
const ConfirmContext = createContext(() => Promise.resolve(true))

export function useConfirm() {
  return useContext(ConfirmContext)
}

export function ConfirmProvider({ children }) {
  const token = useAuthStore(s => s.token)
  const { data: settings } = useQuery({
    queryKey: ['general-settings'],
    queryFn: fetchGeneralSettings,
    enabled: !!token,
    staleTime: 60_000,
    retry: false,
  })
  // Default ON — only an explicit "false" disables confirmations.
  const enabled = settings?.confirm_destructive !== 'false'
  const enabledRef = useRef(enabled)
  useEffect(() => { enabledRef.current = enabled }, [enabled])

  const [dialog, setDialog] = useState(null)
  const resolverRef = useRef(null)

  const confirm = useCallback((opts = {}) => {
    if (!enabledRef.current) return Promise.resolve(true) // setting off → skip dialog
    return new Promise(resolve => {
      resolverRef.current = resolve
      setDialog(opts)
    })
  }, [])

  const close = useCallback((result) => {
    const resolve = resolverRef.current
    resolverRef.current = null
    setDialog(null)
    resolve?.(result)
  }, [])

  // Esc cancels. (Enter is intentionally NOT bound to confirm, to avoid
  // accidentally triggering a destructive action.)
  useEffect(() => {
    if (!dialog) return
    const onKey = e => { if (e.key === 'Escape') close(false) }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [dialog, close])

  const danger = dialog?.danger !== false // default to danger styling

  return (
    <ConfirmContext.Provider value={confirm}>
      {children}
      {dialog && (
        <div
          className="fixed inset-0 z-[60] flex items-center justify-center bg-black/60 backdrop-blur-sm"
          onClick={() => close(false)}
        >
          <div
            className="bg-surface border border-border rounded-xl w-full max-w-md mx-4 p-6 space-y-4"
            onClick={e => e.stopPropagation()}
          >
            <h3 className="font-semibold text-content-strong">{dialog.title || 'Are you sure?'}</h3>
            {dialog.message && <p className="text-sm text-content-muted leading-relaxed">{dialog.message}</p>}
            <div className="flex justify-end gap-3 pt-1">
              <button
                onClick={() => close(false)}
                className="px-4 py-2 text-sm rounded-lg border border-border-strong bg-surface-raised hover:bg-surface-overlay text-content transition-colors"
              >
                {dialog.cancelLabel || 'Cancel'}
              </button>
              <button
                onClick={() => close(true)}
                className={`px-4 py-2 text-sm font-semibold rounded-lg text-content-strong transition-colors ${
                  danger ? 'bg-red-700 hover:bg-red-600' : 'bg-brand-600 hover:bg-brand-700'
                }`}
              >
                {dialog.confirmLabel || 'Confirm'}
              </button>
            </div>
          </div>
        </div>
      )}
    </ConfirmContext.Provider>
  )
}
