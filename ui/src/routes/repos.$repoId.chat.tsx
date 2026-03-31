import { useQuery, useMutation } from '@tanstack/react-query'
import { useParams, Link } from 'react-router-dom'
import { ArrowLeft, Send, Plus, Shield, User, AlertCircle, Copy, Check, Loader2, MessageSquare, Code, Search, Cpu } from 'lucide-react'
import { useState, useRef, useEffect, useCallback } from 'react'
import { motion, AnimatePresence } from 'framer-motion'
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

const SUGGESTED = [
  { icon: Cpu, text: "What's the overall architecture?" },
  { icon: Search, text: "How does authentication work?" },
  { icon: Code, text: "What are the main entry points?" },
  { icon: MessageSquare, text: "Explain the service structure" },
]

function CopyButton({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      onClick={async () => {
        await navigator.clipboard.writeText(text)
        setCopied(true)
        setTimeout(() => setCopied(false), 2000)
      }}
      className="absolute top-2 right-2 p-1.5 rounded-md bg-zinc-700/80 hover:bg-zinc-600 text-zinc-300 opacity-0 group-hover/code:opacity-100 transition-all"
      aria-label="Copy code"
    >
      {copied ? <Check className="h-3.5 w-3.5 text-emerald-400" /> : <Copy className="h-3.5 w-3.5" />}
    </button>
  )
}

function CollapsibleCode({ language, code }: { language: string; code: string }) {
  const [expanded, setExpanded] = useState(code.split('\n').length <= 12)

  return (
    <div className="relative group/code my-3">
      <div className="flex items-center justify-between px-4 py-1.5 bg-zinc-900 rounded-t-xl">
        <span className="text-[11px] text-zinc-500 font-mono">{language}</span>
        {!expanded && (
          <button
            onClick={() => setExpanded(true)}
            className="text-[11px] text-primary hover:text-primary/80 font-medium"
          >
            Expand
          </button>
        )}
      </div>
      <div className={`relative ${!expanded ? 'max-h-48 overflow-hidden' : ''}`}>
        <SyntaxHighlighter
          style={oneDark}
          language={language}
          PreTag="div"
          className="!rounded-t-none !rounded-b-xl !text-xs !my-0"
        >
          {code}
        </SyntaxHighlighter>
        {!expanded && (
          <div className="absolute inset-x-0 bottom-0 h-16 bg-gradient-to-t from-[#282c34] to-transparent" />
        )}
        <CopyButton text={code} />
      </div>
      {expanded && code.split('\n').length > 12 && (
        <button
          onClick={() => setExpanded(false)}
          className="w-full text-[11px] text-zinc-500 hover:text-zinc-300 py-1.5 transition-colors"
        >
          Collapse
        </button>
      )}
    </div>
  )
}

const markdownComponents = {
  code({ node, inline, className, children, ...props }: any) {
    const match = /language-(\w+)/.exec(className || '')
    const codeString = String(children).replace(/\n$/, '')
    return !inline && match ? (
      <CollapsibleCode language={match[1]} code={codeString} />
    ) : (
      <code className="bg-accent/50 px-1.5 py-0.5 rounded text-[13px] font-mono text-foreground" {...props}>
        {children}
      </code>
    )
  },
  p: ({ children }: any) => <p className="mb-3 last:mb-0 text-[15px] leading-[1.7]">{children}</p>,
  ul: ({ children }: any) => <ul className="mb-3 ml-5 space-y-1.5 list-disc text-[15px]">{children}</ul>,
  ol: ({ children }: any) => <ol className="mb-3 ml-5 space-y-1.5 list-decimal text-[15px]">{children}</ol>,
  li: ({ children }: any) => <li className="leading-[1.7]">{children}</li>,
  h1: ({ children }: any) => <h1 className="text-lg font-bold mb-3 mt-5 first:mt-0">{children}</h1>,
  h2: ({ children }: any) => <h2 className="text-base font-bold mb-2 mt-5 first:mt-0">{children}</h2>,
  h3: ({ children }: any) => <h3 className="text-[15px] font-semibold mb-2 mt-4 first:mt-0">{children}</h3>,
  strong: ({ children }: any) => <strong className="font-semibold text-foreground">{children}</strong>,
  blockquote: ({ children }: any) => (
    <blockquote className="border-l-2 border-primary/30 pl-3 my-2 text-muted-foreground italic">{children}</blockquote>
  ),
  table: ({ children }: any) => (
    <div className="overflow-x-auto my-3"><table className="text-sm border-collapse w-full">{children}</table></div>
  ),
  th: ({ children }: any) => <th className="border border-border/30 px-3 py-1.5 text-left font-semibold bg-accent/30">{children}</th>,
  td: ({ children }: any) => <td className="border border-border/30 px-3 py-1.5">{children}</td>,
}

const msgVariants = {
  initial: { opacity: 0, y: 12, scale: 0.97 },
  animate: { opacity: 1, y: 0, scale: 1, transition: { duration: 0.3 } },
  exit: { opacity: 0 },
}

function TypingDots() {
  return (
    <div className="flex gap-3 items-start">
      <div className="h-8 w-8 rounded-xl bg-primary/10 flex items-center justify-center shrink-0 mt-0.5">
        <Shield className="h-4 w-4 text-primary" />
      </div>
      <div className="bg-card rounded-2xl rounded-tl-md px-4 py-3">
        <div className="flex gap-1.5 items-center h-4">
          <span className="h-2 w-2 bg-primary/50 rounded-full animate-bounce [animation-delay:-0.3s]" />
          <span className="h-2 w-2 bg-primary/50 rounded-full animate-bounce [animation-delay:-0.15s]" />
          <span className="h-2 w-2 bg-primary/50 rounded-full animate-bounce" />
        </div>
      </div>
    </div>
  )
}

export default function ChatPage() {
  const { repoId } = useParams<{ repoId: string }>()
  const [messages, setMessages] = useState<Message[]>([])
  const [input, setInput] = useState('')
  const messagesEndRef = useRef<HTMLDivElement>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  const id = parseInt(repoId ?? '0', 10)

  const { data: repo, isLoading: repoLoading } = useQuery<Repository>({
    queryKey: ['repo', repoId],
    queryFn: () => api.repos.get(id),
    enabled: !!repoId,
  })

  const chat = useMutation({
    mutationFn: (question: string) => {
      const history = messages.map((m) => m.role === 'user' ? `User: ${m.content}` : `AI: ${m.content}`)
      return api.chat.ask(id, { question, history })
    },
    onSuccess: (res) => {
      setMessages((prev) => [...prev, { id: Date.now().toString(), role: 'assistant', content: res.answer }])
    },
    onError: (err) => {
      setMessages((prev) => [...prev, {
        id: Date.now().toString(), role: 'assistant',
        content: err instanceof Error ? err.message : 'Failed to get response', isError: true,
      }])
    },
  })

  const explain = useMutation({
    mutationFn: (path: string) => api.chat.explain(id, { path }),
    onSuccess: (res) => {
      setMessages((prev) => [...prev, { id: Date.now().toString(), role: 'assistant', content: res.content }])
    },
    onError: (err) => {
      setMessages((prev) => [...prev, {
        id: Date.now().toString(), role: 'assistant',
        content: err instanceof Error ? err.message : 'Failed to explain', isError: true,
      }])
    },
  })

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, chat.isPending, explain.isPending])

  const handleInput = (e: React.FormEvent<HTMLTextAreaElement>) => {
    const ta = e.currentTarget
    ta.style.height = 'auto'
    ta.style.height = Math.min(ta.scrollHeight, 160) + 'px'
  }

  const submit = useCallback((text: string) => {
    const q = text.trim()
    if (!q || chat.isPending || explain.isPending) return
    setInput('')
    if (textareaRef.current) textareaRef.current.style.height = 'auto'

    setMessages((prev) => [...prev, { id: Date.now().toString(), role: 'user', content: q }])

    if (q.startsWith('/explain ')) {
      explain.mutate(q.slice('/explain '.length).trim())
    } else {
      chat.mutate(q)
    }
  }, [chat, explain])

  const handleKeyDown = (e: React.KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); submit(input) }
  }

  const [org, repoName] = (repo?.full_name ?? '/').split('/')
  const isThinking = chat.isPending || explain.isPending

  if (repoLoading) {
    return (
      <div className="h-screen flex flex-col bg-background items-center justify-center">
        <Loader2 className="h-8 w-8 animate-spin text-primary mb-3" />
        <p className="text-sm text-muted-foreground">Loading chat...</p>
      </div>
    )
  }

  return (
    <div className="h-full flex flex-col bg-background">
      {/* Header */}
      <header className="border-b border-border/30 bg-surface px-6 py-3 shrink-0">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <Link
              to={`/repos/${repoId}`}
              className="p-1.5 rounded-lg hover:bg-accent/50 text-muted-foreground transition-colors"
              aria-label="Back"
            >
              <ArrowLeft className="h-4 w-4" />
            </Link>
            <div className="flex items-center gap-2.5">
              <div className="h-7 w-7 rounded-lg bg-primary/10 flex items-center justify-center">
                <Shield className="h-3.5 w-3.5 text-primary" />
              </div>
              <div>
                <h1 className="font-semibold text-foreground text-sm leading-none">
                  <span className="text-muted-foreground font-normal">{org}/</span>{repoName}
                </h1>
                <p className="text-[11px] text-muted-foreground/60 mt-0.5">
                  AI Chat · {messages.filter(m => m.role === 'user').length} messages
                </p>
              </div>
            </div>
          </div>
          {messages.length > 0 && (
            <button
              onClick={() => setMessages([])}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs text-muted-foreground hover:text-foreground hover:bg-accent/50 transition-colors"
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
          /* Empty state */
          <div className="flex-1 flex flex-col items-center justify-center px-4 py-20 animate-fade-in">
            <div className="text-center max-w-xl w-full">
              <div className="relative mx-auto mb-5 w-fit">
                <div className="absolute inset-0 rounded-2xl bg-primary/10 blur-xl scale-150" />
                <div className="relative h-14 w-14 rounded-2xl bg-primary/10 flex items-center justify-center">
                  <Shield className="h-7 w-7 text-primary" />
                </div>
              </div>
              <h2 className="text-xl font-semibold mb-2 text-foreground">
                Ask about <span className="text-primary">{repo?.full_name}</span>
              </h2>
              <p className="text-muted-foreground text-sm mb-8">
                Architecture, patterns, implementation details — or{' '}
                <code className="font-mono text-xs bg-accent/50 px-1.5 py-0.5 rounded text-foreground">/explain &lt;path&gt;</code>
              </p>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 text-sm">
                {SUGGESTED.map(({ icon: Icon, text }) => (
                  <motion.button
                    key={text}
                    whileHover={{ scale: 1.02 }}
                    whileTap={{ scale: 0.98 }}
                    onClick={() => submit(text)}
                    className="flex items-start gap-3 text-left px-4 py-3 rounded-xl bg-card border border-border shadow-sm hover:shadow-md dark:border-transparent dark:shadow-none hover:bg-muted/50 dark:hover:bg-accent/40 text-muted-foreground hover:text-foreground transition-all group"
                  >
                    <Icon className="h-4 w-4 text-primary/50 group-hover:text-primary shrink-0 mt-0.5 transition-colors" />
                    <span>{text}</span>
                  </motion.button>
                ))}
              </div>
            </div>
          </div>
        ) : (
          <div className="max-w-3xl mx-auto px-4 py-6 space-y-5">
            <AnimatePresence mode="popLayout">
              {messages.map((msg) => (
                <motion.div
                  key={msg.id}
                  layout
                  variants={msgVariants}
                  initial="initial"
                  animate="animate"
                  exit="exit"
                >
                  {msg.role === 'user' ? (
                    <div className="flex gap-3 items-start justify-end">
                      <div className="max-w-[80%] bg-primary rounded-2xl rounded-tr-md px-5 py-3.5">
                        <p className="text-[15px] text-primary-foreground whitespace-pre-wrap leading-[1.7]">{msg.content}</p>
                      </div>
                      <div className="h-8 w-8 rounded-xl bg-accent flex items-center justify-center shrink-0 mt-0.5">
                        <User className="h-4 w-4 text-muted-foreground" />
                      </div>
                    </div>
                  ) : (
                    <div className="flex gap-3 items-start">
                      <div className="h-8 w-8 rounded-xl bg-primary/10 flex items-center justify-center shrink-0 mt-0.5">
                        <Shield className="h-4 w-4 text-primary" />
                      </div>
                      <div className="flex-1 min-w-0">
                        {msg.isError ? (
                          <div className="flex items-start gap-2 text-sm text-red-400 bg-red-500/10 rounded-xl px-4 py-3">
                            <AlertCircle className="h-4 w-4 shrink-0 mt-0.5" />
                            <span>{msg.content}</span>
                          </div>
                        ) : (
                          <div className="text-[15px] text-foreground/90 leading-[1.7]">
                            <ReactMarkdown components={markdownComponents}>{msg.content}</ReactMarkdown>
                          </div>
                        )}
                      </div>
                    </div>
                  )}
                </motion.div>
              ))}
            </AnimatePresence>
            {isThinking && <TypingDots />}
            <div ref={messagesEndRef} />
          </div>
        )}
      </ScrollArea>

      {/* Input bar */}
      <div className="border-t border-border/30 bg-surface px-4 py-4 shrink-0">
        <form onSubmit={(e) => { e.preventDefault(); submit(input) }} className="max-w-3xl mx-auto">
          <div className="flex gap-2 items-end bg-card rounded-xl px-3 py-2 focus-within:ring-1 focus-within:ring-primary/20 transition-all">
            <textarea
              ref={textareaRef}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onInput={handleInput}
              onKeyDown={handleKeyDown}
              placeholder="Ask a question… or /explain <path>"
              rows={1}
              aria-label="Chat message"
              className="flex-1 bg-transparent text-foreground text-sm leading-relaxed placeholder:text-muted-foreground/40 focus:outline-none resize-none py-1"
              disabled={isThinking}
            />
            <button
              type="submit"
              disabled={!input.trim() || isThinking}
              className="p-2 rounded-lg bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-30 disabled:cursor-not-allowed transition-colors shrink-0 mb-0.5"
              aria-label="Send"
            >
              <Send className="h-3.5 w-3.5" />
            </button>
          </div>
          <p className="text-[11px] text-muted-foreground/40 mt-2 text-center">
            <kbd className="font-mono bg-accent/30 px-1.5 py-0.5 rounded-md text-xs">Enter</kbd> to send ·{' '}
            <kbd className="font-mono bg-accent/30 px-1.5 py-0.5 rounded-md text-xs">Shift+Enter</kbd> new line ·{' '}
            <code className="font-mono text-muted-foreground/50">/explain &lt;path&gt;</code>
          </p>
        </form>
      </div>
    </div>
  )
}
