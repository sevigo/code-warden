import * as React from "react"
import { cn } from "@/lib/utils"

/**
 * HashiCorp-Inspired Card Component
 * 
 * Key patterns:
 * - 8px border-radius
 * - Micro-shadows at 0.05 opacity
 * - Subtle border on light mode
 * - Clean, minimal appearance
 */

const Card = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement> & { 
    variant?: 'default' | 'elevated' | 'bordered' | 'ghost'
    hover?: boolean 
  }
>(({ className, variant = 'default', hover = false, ...props }, ref) => {
  const variantStyles = {
    default: 'bg-card text-card-foreground border border-border shadow-[0_1px_1px_rgba(97,104,117,0.05),0_2px_2px_rgba(97,104,117,0.05)]',
    elevated: 'bg-card text-card-foreground border border-border shadow-[0_4px_6px_-1px_rgba(97,104,117,0.05),0_2px_4px_-1px_rgba(97,104,117,0.05)]',
    bordered: 'bg-card text-card-foreground border border-border shadow-none',
    ghost: 'bg-transparent text-card-foreground border-none shadow-none',
  }
  
  return (
    <div
      ref={ref}
      className={cn(
        'rounded-[8px]',
        variantStyles[variant],
        hover && variant !== 'ghost' && 'transition-all duration-150 hover:shadow-[0_4px_6px_-1px_rgba(97,104,117,0.05),0_2px_4px_-1px_rgba(97,104,117,0.05)]',
        className
      )}
      {...props}
    />
  )
})
Card.displayName = "Card"

const CardHeader = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, ...props }, ref) => (
  <div
    ref={ref}
    className={cn("flex flex-col space-y-1.5 p-5", className)}
    {...props}
  />
))
CardHeader.displayName = "CardHeader"

const CardTitle = React.forwardRef<
  HTMLHeadingElement,
  React.HTMLAttributes<HTMLHeadingElement>
>(({ className, ...props }, ref) => (
  <h3
    ref={ref}
    className={cn(
      "font-semibold leading-tight tracking-normal text-foreground",
      "text-base"
    )}
    {...props}
  />
))
CardTitle.displayName = "CardTitle"

const CardDescription = React.forwardRef<
  HTMLParagraphElement,
  React.HTMLAttributes<HTMLParagraphElement>
>(({ className, ...props }, ref) => (
  <p
    ref={ref}
    className={cn("text-sm text-muted-foreground leading-relaxed", className)}
    {...props}
  />
))
CardDescription.displayName = "CardDescription"

const CardContent = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, ...props }, ref) => (
  <div ref={ref} className={cn("p-5 pt-0", className)} {...props} />
))
CardContent.displayName = "CardContent"

const CardFooter = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, ...props }, ref) => (
  <div
    ref={ref}
    className={cn("flex items-center p-5 pt-0", className)}
    {...props}
  />
))
CardFooter.displayName = "CardFooter"

/**
 * KPI Card - For dashboard metrics
 * Follows HashiCorp's dense heading, spacious body pattern
 */
const KPICard = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement> & {
    icon: React.ElementType
    label: string
    value: string | number
    sub?: string
    trend?: 'up' | 'down' | 'neutral'
    trendValue?: string
    color?: string
  }
>(({ className, icon: Icon, label, value, sub, trend, trendValue, color, ...props }, ref) => {
  const trendIcon = trend === 'up' ? '↑' : trend === 'down' ? '↓' : '→'
  const trendColor = trend === 'up' 
    ? 'text-emerald-500' 
    : trend === 'down' 
      ? 'text-rose-500' 
      : 'text-muted-foreground'
  
  return (
    <Card 
      ref={ref} 
      variant="default" 
      hover 
      className={cn("p-5", className)} 
      {...props}
    >
      <div className="flex flex-col gap-3">
        <div className={cn("h-8 w-8 rounded-[6px] flex items-center justify-center bg-primary/10", color || "text-primary")}>
          <Icon className="h-4 w-4" />
        </div>
        
        <div className="space-y-1">
          <div className="flex items-baseline gap-2">
            <span className="text-2xl font-bold text-foreground font-mono tracking-tight">
              {value}
            </span>
            {trend && (
              <span className={cn("text-xs font-medium", trendColor)}>
                {trendIcon} {trendValue}
              </span>
            )}
          </div>
          <p className="text-xs text-muted-foreground">{label}</p>
          {sub && (
            <p className="text-[11px] text-muted-foreground/60">{sub}</p>
          )}
        </div>
      </div>
    </Card>
  )
})
KPICard.displayName = "KPICard"

/**
 * Action Card - For CTAs and navigation
 */
interface ActionCardProps {
  icon: React.ElementType
  title: string
  description: string
  actionLabel: string
  href?: string
  onClick?: () => void
  className?: string
}

const ActionCard = ({ className, icon: Icon, title, description, actionLabel, href, onClick }: ActionCardProps) => {
  const baseClasses = cn(
    "block p-5 rounded-[8px] cursor-pointer",
    "border border-border",
    "transition-all duration-150",
    "hover:shadow-[0_4px_6px_-1px_rgba(97,104,117,0.05),0_2px_4px_-1px_rgba(97,104,117,0.05)]",
    "hover:border-[#b2b6bd] dark:hover:border-[#3b3d45]",
    className
  )
  
  const content = (
    <div className="flex items-start gap-4">
      <div className="h-9 w-9 rounded-[6px] flex items-center justify-center shrink-0 bg-primary/10 text-primary">
        <Icon className="h-4 w-4" />
      </div>
      
      <div className="flex-1 min-w-0">
        <h3 className="font-semibold text-foreground text-sm mb-1">{title}</h3>
        <p className="text-sm text-muted-foreground mb-3 leading-relaxed">{description}</p>
        <span className="inline-flex items-center gap-1.5 text-sm font-medium text-primary hover:text-primary/80 transition-colors">
          {actionLabel}
          <svg className="h-4 w-4" fill="none" viewBox="0 0 24 24" stroke="currentColor">
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M9 5l7 7-7 7" />
          </svg>
        </span>
      </div>
    </div>
  )
  
  if (href) {
    return (
      <a
        href={href}
        className={baseClasses}
        onClick={onClick}
      >
        {content}
      </a>
    )
  }
  
  return (
    <div
      className={baseClasses}
      onClick={onClick}
    >
      {content}
    </div>
  )
}

export { 
  Card, 
  CardHeader, 
  CardFooter, 
  CardTitle, 
  CardDescription, 
  CardContent,
  KPICard,
  ActionCard,
}
