import { useQuery, useMutation } from '@tanstack/react-query'
import { useParams, Link } from 'react-router-dom'
import { ArrowLeft, Send, Plus, Shield, User, AlertCircle } from 'lucide-react'
import { useState, useRef, useEffect, useCallback } from 'react'
import ReactMarkdown from 'react-markdown'
import { Prism as SyntaxHighlighter } from 'react-syntax-highlighter'
import { oneDark } from 'react-syntax-highlighter/dist/esm/styles/prism'
import { ScrollArea } from '@/components/ui/scroll-area'
import { api } from '@/lib/api'
import type { Repository } from '@/lib/api'

interface Message {
  id: string
  role: 'user' | 'assistant'
  content: string
  isError?: boolean
}

const SUGGESTED_QUESTIONS = [
  "What's the overall architecture of this project?",
  "How does authentication work here?",
  "What are the main entry points?",
  "Explain the main service structure",
]

const markdownComponents = {
  code({ node, inline, className, children, ...props }: any) {
    const match = /language-(\w+)/.exec(className || '')
    return !inline && match ? (
      <SyntaxHighlighter
        style={oneDark}
        language={match[1]}
        PreTag="div"
        className="!rounded-lg !text-xs !my-3"
        {...props}
      >
        {String(children).replace(/\n$/, '')}
      </SyntaxHighlighter>
    ) : (
      <code
        className="bg-zinc-700/60 px-1.5 py-0.5 rounded text-xs font-mono text-zinc-200"
        {...props}
      >
        {children}
      </code>
    )
  },
  p: ({ children }: any) => <p className="mb-3 last:mb-0 leading-relaxed">{children}</p>,
  ul: ({ children }: any) => <ul className="mb-3 ml-4 space-y-1 list-disc">{children}</ul>,
  ol: ({ children }: any) => <ol className="mb-3 ml-4 space-y-1 list-decimal">{children}</ol>,
  li: ({ children }: any) => <li className="leading-relaxed">{children}</li>,
  h1: ({ children }: any) => <h1 className="text-base font-bold mb-2 mt-4 first:mt-0">{children}</h1>,
  h2: ({ children }: any) => <h2 className="text-sm font-bold mb-2 mt-4 first:mt-0">{children}</h2>,
  h3: ({ children }: any) => <h3 className="text-sm font-semibold mb-1.5 mt-3 first:mt-0">{children}</h3>,
  strong: ({ children }: any) => <strong className="font-semibold text-zinc-100">{children}</strong>,
  blockquote: ({ children }: any) => (
    <blockquote className="border-l-2 border-zinc-600 pl-3 my-2 text-zinc-400 italic">{children}</blockquote>
  ),
  table: ({ children }: any) => (
    <div className="overflow-x-auto my-3">
      <table className="text-xs border-collapse w-full">{children}</table>
    </div>
  ),
  th: ({ children }: any) => (
    <th className="border border-zinc-700 px-3 py-1.5 text-left font-semibold bg-zinc-800">{children}</th>
  ),
  td: ({ children }: any) => (
    <td className="border border-zinc-700 px-3 py-1.5">{children}</td>
  ),
}

function AIAvatar() {
  return (
    <div className="h-8 w-8 rounded-lg bg-primary/20 border border-primary/30 flex items-center justify-center shrink-0 mt-0.5">
      <Shield className="h-4 w-4 text-primary" />
    </div>
  )
}

function UserAvatar() {
  return (
    <div className="h-8 w-8 rounded-lg bg-zinc-700 border border-zinc-600 flex items-center justify-center shrink-0 mt-0.5">
      <User className="h-4 w-4 text-zinc-300" />
    </div>
  )
}

function TypingIndicator() {
  return (
    <div className="flex gap-3 items-start">
      <AIAvatar />
      <div className="bg-zinc-800/60 border border-zinc-700/50 rounded-2xl rounded-tl-sm px-4 py-3">
        <div className="flex gap-1 items-center h-4">
          <span className="h-1.5 w-1.5 bg-zinc-400 rounded-full animate-bounce [animation-delay:-0.3s]" />
          <span className="h-1.5 w-1.5 bg-zinc-400 rounded-full animate-bounce [animation-delay:-0.15s]" />
          <span className="h-1.5 w-1.5 bg-zinc-400 rounded-full animate-bounce" />
        </div>
      </div>
    </div>
  )
}

function ChatPage() {
  const { repoId } = useParams<{ repoId: string }>()
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState('')
  const messagesEndRef = useRef<HTMLDivElement>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const id = parseInt(repoId ?? '0', 10)

  const { data: repo } = useQuery<Repository>({
    queryKey: ['repo', repoId],
    queryFn: () => api.repos.get(id),
    enabled: !!repoId,
  })

  const chat = useMutation({
    mutationFn: (question: string) => {
      const history = messages.map((m) =>
        m.role === 'user' ? `User: ${m.content}` : `AI: ${m.content}`
      )
      return api.chat.ask(id, { question, history })
    },
    onSuccess: (response) => {
      setMessages((prev) => [
        ...prev,
        { id: Date.now().toString(), role: 'assistant', content: response.answer },
      ])
    },
    onError: (error) => {
      setMessages((prev) => [
        ...prev,
        {
          id: Date.now().toString(),
          role: 'assistant',
          content: error instanceof Error ? error.message : 'Failed to get response',
          isError: true,
        },
      ])
    },
  })

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, chat.isPending])

  const handleTextareaInput = (e: React.FormEvent<HTMLTextAreaElement>) => {
    const ta = e.currentTarget
    ta.style.height = 'auto'
    ta.style.height = Math.min(ta.scrollHeight, 160) + 'px'
  }

  const submitMessage = useCallback(
    (text: string) => {
      const question = text.trim()
      if (!question || chat.isPending) return

      setInput('')
      if (textareaRef.current) {
        textareaRef.current.style.height = 'auto'
      }

      if (question.startsWith('/explain ')) {
        const path = question.slice('/explain '.length).trim()
        setMessages((prev) => [
          ...prev,
          { id: Date.now().toString(), role: 'user', content: question },
        ])
        api.chat.explain(id, { path }).then((response) => {
          setMessages((prev) => [
            ...prev,
            { id: (Date.now() + 1).toString(), role: 'assistant', content: response.content },
          ])
        }).catch((err) => {
          setMessages((prev) => [
            ...prev,
            {
              id: (Date.now() + 1).toString(),
              role: 'assistant',
              content: err instanceof Error ? err.message : 'Failed to explain path',
              isError: true,
            },
          ])
        })
        return
      }

      setMessages((prev) => [
        ...prev,
        { id: Date.now().toString(), role: 'user', content: question },
      ])
      chat.mutate(question)
    },
    [chat, id]
  )

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      submitMessage(input)
    }
  }

  const handleFormSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    submitMessage(input)
  }

  const [org, repoName] = (repo?.full_name ?? '/').split('/')

  return (
    <div className="h-screen flex flex-col bg-background">
      {/* Header */}
      <header className="border-b border-zinc-800 bg-zinc-900/80 backdrop-blur px-4 py-3 shrink-0">
        <div className="max-w-3xl mx-auto flex items-center justify-between">
          <div className="flex items-center gap-3">
            <Link
              to={`/repos/${repoId}`}
              className="p-1.5 rounded-lg hover:bg-zinc-800 text-zinc-400 transition-colors"
            >
              <ArrowLeft className="h-4 w-4" />
            </Link>
            <div className="flex items-center gap-2.5">
              <div className="h-7 w-7 rounded-md bg-primary/20 border border-primary/30 flex items-center justify-center">
                <Shield className="h-3.5 w-3.5 text-primary" />
              </div>
              <div>
                <h1 className="font-semibold text-foreground text-sm leading-none">
                  <span className="text-zinc-500 font-normal">{org}/</span>{repoName}
                </h1>
                <p className="text-[11px] text-zinc-500 mt-0.5">AI Chat · {messages.filter(m => m.role === 'user').length} messages</p>
              </div>
            </div>
          </div>

          {messages.length > 0 && (
            <button
              onClick={() => setMessages([])}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800 transition-colors"
            >
              <Plus className="h-3.5 w-3.5" />
              New chat
            </button>
          )}
        </div>
      </header>

      {/* Messages */}
      <ScrollArea className="flex-1">
        {messages.length === 0 ? (
          <div className="flex items-center justify-center px-4 py-24">
            <div className="text-center max-w-lg w-full">
              <div className="h-14 w-14 rounded-2xl bg-primary/10 border border-primary/20 flex items-center justify-center mx-auto mb-5">
                <Shield className="h-7 w-7 text-primary" />
              </div>
              <h2 className="text-xl font-semibold mb-2 text-foreground">
                Ask about <span className="text-primary">{repo?.full_name ?? 'this repository'}</span>
              </h2>
              <p className="text-muted-foreground text-sm mb-8">
                Architecture, patterns, implementation details — or use{' '}
                <code className="font-mono text-xs bg-zinc-800 px-1.5 py-0.5 rounded text-zinc-300">/explain &lt;path&gt;</code>{' '}
                for file-level context.
              </p>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 text-sm">
                {SUGGESTED_QUESTIONS.map((q) => (
                  <button
                    key={q}
                    onClick={() => submitMessage(q)}
                    className="text-left px-4 py-3 rounded-xl bg-zinc-900 border border-zinc-800 hover:border-primary/40 hover:bg-zinc-800/60 text-zinc-300 transition-all duration-150"
                  >
                    {q}
                  </button>
                ))}
              </div>
            </div>
          </div>
        ) : (
          <div className="max-w-3xl mx-auto px-4 py-6 space-y-6">
            {messages.map((message) => (
              <div key={message.id}>
                {message.role === 'user' ? (
                  /* User message */
                  <div className="flex gap-3 items-start justify-end">
                    <div className="max-w-[80%] bg-primary rounded-2xl rounded-tr-sm px-4 py-3">
                      <p className="text-sm text-primary-foreground whitespace-pre-wrap leading-relaxed">
                        {message.content}
                      </p>
                    </div>
                    <UserAvatar />
                  </div>
                ) : (
                  /* AI message */
                  <div className="flex gap-3 items-start">
                    <AIAvatar />
                    <div className="flex-1 min-w-0">
                      {message.isError ? (
                        <div className="flex items-start gap-2 text-sm text-red-400 bg-red-500/10 border border-red-500/20 rounded-xl px-4 py-3">
                          <AlertCircle className="h-4 w-4 shrink-0 mt-0.5" />
                          <span>{message.content}</span>
                        </div>
                      ) : (
                        <div className="text-sm text-zinc-200 leading-relaxed">
                          <ReactMarkdown components={markdownComponents}>
                            {message.content}
                          </ReactMarkdown>
                        </div>
                      )}
                    </div>
                  </div>
                )}
              </div>
            ))}

            {chat.isPending && <TypingIndicator />}
            <div ref={messagesEndRef} />
          </div>
        )}
      </ScrollArea>

      {/* Input */}
      <div className="border-t border-zinc-800 bg-zinc-900/80 backdrop-blur px-4 py-4 shrink-0">
        <form onSubmit={handleFormSubmit} className="max-w-3xl mx-auto">
          <div className="flex gap-2 items-end bg-zinc-800 border border-zinc-700 rounded-xl px-3 py-2 focus-within:border-primary/50 focus-within:ring-1 focus-within:ring-primary/20 transition-all">
            <textarea
              ref={textareaRef}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onInput={handleTextareaInput}
              onKeyDown={handleKeyDown}
              placeholder="Ask a question… or /explain <path>"
              rows={1}
              className="flex-1 bg-transparent text-foreground text-sm leading-relaxed placeholder:text-zinc-600 focus:outline-none resize-none py-1"
              disabled={chat.isPending}
            />
            <button
              type="submit"
              disabled={!input.trim() || chat.isPending}
              className="p-2 rounded-lg bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-40 disabled:cursor-not-allowed transition-colors shrink-0 mb-0.5"
            >
              <Send className="h-3.5 w-3.5" />
            </button>
          </div>
          <p className="text-[11px] text-zinc-600 mt-2 text-center">
            Enter to send · Shift+Enter for new line · <code className="font-mono">/explain &lt;path&gt;</code> for file context
          </p>
        </form>
      </div>
    </div>
  )
}

export default ChatPage
