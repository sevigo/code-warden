import { useQuery, useMutation } from '@tanstack/react-query'
import { useParams, Link } from 'react-router-dom'
import { ArrowLeft, Send, Plus } from 'lucide-react'
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

function TypingIndicator() {
  return (
    <div className="flex justify-start">
      <div className="bg-muted rounded-2xl rounded-tl-sm px-4 py-3">
        <div className="flex gap-1 items-center h-4">
          <span className="h-2 w-2 bg-muted-foreground rounded-full animate-bounce [animation-delay:-0.3s]" />
          <span className="h-2 w-2 bg-muted-foreground rounded-full animate-bounce [animation-delay:-0.15s]" />
          <span className="h-2 w-2 bg-muted-foreground rounded-full animate-bounce" />
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
          content: `Error: ${error instanceof Error ? error.message : 'Failed to get response'}`,
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

      // Handle /explain command
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
              content: `Error: ${err instanceof Error ? err.message : 'Failed to explain path'}`,
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

  return (
    <div className="h-screen flex flex-col bg-background">
      {/* Header */}
      <header className="border-b border-border bg-card px-4 py-3 shrink-0">
        <div className="max-w-3xl mx-auto flex items-center justify-between">
          <div className="flex items-center gap-3">
            <Link
              to={`/repos/${repoId}`}
              className="p-1.5 rounded-md hover:bg-muted text-muted-foreground transition-colors"
            >
              <ArrowLeft className="h-5 w-5" />
            </Link>
            <div>
              <h1 className="font-semibold text-foreground text-sm">
                {repo?.full_name ?? 'Loading...'}
              </h1>
              <p className="text-xs text-muted-foreground">AI Chat</p>
            </div>
          </div>

          {messages.length > 0 && (
            <button
              onClick={() => setMessages([])}
              className="flex items-center gap-1.5 px-3 py-1.5 rounded-md text-xs text-muted-foreground hover:bg-muted transition-colors"
            >
              <Plus className="h-3.5 w-3.5" />
              New conversation
            </button>
          )}
        </div>
      </header>

      {/* Messages */}
      <ScrollArea className="flex-1">
        {messages.length === 0 ? (
          <div className="h-full flex items-center justify-center px-4 py-20">
            <div className="text-center max-w-md w-full">
              <h2 className="text-xl font-semibold mb-2 text-foreground">
                Explore {repo?.full_name ?? 'this repository'}
              </h2>
              <p className="text-muted-foreground text-sm mb-6">
                Ask questions about the codebase architecture, patterns, and functionality.
              </p>
              <div className="space-y-2 text-sm">
                {SUGGESTED_QUESTIONS.map((q) => (
                  <button
                    key={q}
                    onClick={() => submitMessage(q)}
                    className="block w-full text-left px-4 py-3 rounded-lg bg-card border border-border hover:border-primary/50 hover:bg-accent text-foreground transition-colors"
                  >
                    {q}
                  </button>
                ))}
              </div>
            </div>
          </div>
        ) : (
          <div className="max-w-3xl mx-auto px-4 py-6 space-y-4">
            {messages.map((message) => (
              <div
                key={message.id}
                className={`flex ${message.role === 'user' ? 'justify-end' : 'justify-start'}`}
              >
                <div
                  className={`max-w-[80%] rounded-2xl px-4 py-3 text-sm ${
                    message.role === 'user'
                      ? 'bg-primary text-primary-foreground rounded-tr-sm'
                      : message.isError
                        ? 'bg-red-500/10 text-red-400 rounded-tl-sm'
                        : 'bg-muted text-foreground rounded-tl-sm'
                  }`}
                >
                  {message.role === 'assistant' ? (
                    <div className="prose prose-sm dark:prose-invert max-w-none">
                      <ReactMarkdown
                        components={{
                          code({ node, inline, className, children, ...props }: any) {
                            const match = /language-(\w+)/.exec(className || '')
                            return !inline && match ? (
                              <SyntaxHighlighter
                                style={oneDark}
                                language={match[1]}
                                PreTag="div"
                                className="rounded-md text-sm"
                                {...props}
                              >
                                {String(children).replace(/\n$/, '')}
                              </SyntaxHighlighter>
                            ) : (
                              <code className="bg-muted px-1.5 py-0.5 rounded text-sm font-mono" {...props}>
                                {children}
                              </code>
                            )
                          },
                        }}
                      >
                        {message.content}
                      </ReactMarkdown>
                    </div>
                  ) : (
                    <p className="whitespace-pre-wrap">{message.content}</p>
                  )}
                </div>
              </div>
            ))}

            {chat.isPending && <TypingIndicator />}
            <div ref={messagesEndRef} />
          </div>
        )}
      </ScrollArea>

      {/* Input */}
      <div className="border-t border-border bg-card px-4 py-3 shrink-0">
        <form onSubmit={handleFormSubmit} className="max-w-3xl mx-auto flex gap-2 items-end">
          <textarea
            ref={textareaRef}
            value={input}
            onChange={(e) => setInput(e.target.value)}
            onInput={handleTextareaInput}
            onKeyDown={handleKeyDown}
            placeholder="Ask a question... (Enter to send, Shift+Enter for newline, /explain <path> for file explanation)"
            rows={1}
            className="flex-1 px-4 py-2.5 rounded-xl border border-border bg-background text-foreground focus:outline-none focus:ring-2 focus:ring-primary resize-none text-sm leading-relaxed placeholder:text-muted-foreground"
            disabled={chat.isPending}
          />
          <button
            type="submit"
            disabled={!input.trim() || chat.isPending}
            className="px-3 py-2.5 rounded-xl bg-primary text-primary-foreground hover:bg-primary/90 disabled:opacity-50 transition-colors shrink-0"
          >
            <Send className="h-4 w-4" />
          </button>
        </form>
        <p className="max-w-3xl mx-auto text-xs text-muted-foreground mt-1.5 pl-1">
          Press Enter to send · Shift+Enter for new line · Use <code>/explain &lt;path&gt;</code> to explain a file or directory
        </p>
      </div>
    </div>
  )
}

export default ChatPage
