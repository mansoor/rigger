import { Component } from 'react'

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
      <div className="min-h-screen bg-gray-950 flex items-center justify-center p-6">
        <div className="max-w-lg w-full bg-gray-900 border border-gray-800 rounded-xl p-6 space-y-3">
          <h1 className="text-lg font-semibold text-white">Something went wrong</h1>
          <p className="text-sm text-gray-400">
            This view hit an unexpected error and couldn't render. Reloading usually clears it; your data is unaffected.
          </p>
          <pre className="text-xs text-red-300/80 bg-red-950/30 border border-red-800/40 rounded-lg p-3 overflow-auto max-h-40 whitespace-pre-wrap font-mono">
            {String(error?.stack || error)}
          </pre>
          <div className="flex gap-2">
            <button
              onClick={() => window.location.reload()}
              className="px-4 py-2 text-sm font-semibold rounded-lg bg-brand-600 hover:bg-brand-700 text-white transition-colors"
            >
              Reload
            </button>
            <button
              onClick={() => this.setState({ error: null })}
              className="px-4 py-2 text-sm rounded-lg border border-gray-700 text-gray-300 hover:text-white hover:bg-gray-800 transition-colors"
            >
              Try again
            </button>
          </div>
        </div>
      </div>
    )
  }
}
