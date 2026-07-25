import { Component } from 'react'
import { Btn } from './ui'

// ErrorBoundary catches render-time exceptions in the routed page subtree so a
// single broken view degrades to a recoverable message instead of white-screening
// the whole app. Reset it by changing its `key` (App keys it by route, so simply
// navigating away clears the error).
export default class ErrorBoundary extends Component {
  state = { error: null }

  static getDerivedStateFromError(error) {
    return { error }
  }

  componentDidCatch(error, info) {
    // Surface it for debugging; the fallback already shows the stack.
    console.error('Render error caught by ErrorBoundary:', error, info?.componentStack)
  }

  render() {
    const { error } = this.state
    if (!error) return this.props.children

    return (
      <div className="min-h-screen bg-canvas flex items-center justify-center p-6">
        <div className="max-w-lg w-full bg-surface border border-border rounded-xl p-6 space-y-3">
          <h1 className="text-lg font-semibold text-content-strong">Something went wrong</h1>
          <p className="text-sm text-content-muted">
            This view hit an unexpected error and couldn't render. Reloading usually clears it; your data is unaffected.
          </p>
          <pre className="text-xs text-danger-fg/80 bg-danger-subtle/30 border border-danger-border/40 rounded-lg p-3 overflow-auto max-h-40 whitespace-pre-wrap font-mono">
            {String(error?.stack || error)}
          </pre>
          <div className="flex gap-2">
            <Btn variant="primary" size="md" onClick={() => window.location.reload()}
              
            >
              Reload
            </Btn>
            <Btn variant="secondary" size="md" onClick={() => this.setState({ error: null })}
              
            >
              Try again
            </Btn>
          </div>
        </div>
      </div>
    )
  }
}
