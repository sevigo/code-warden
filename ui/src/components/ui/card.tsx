import * as React from "react"
import { cn } from "@/lib/utils"

const Card = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, ...props }, ref) => {
  return (
    <div
      ref={ref}
      className={cn(
        'rounded-[8px] bg-card text-card-foreground border border-border shadow-[0_1px_1px_rgba(97,104,117,0.05),0_2px_2px_rgba(97,104,117,0.05)]',
        className
      )}
      {...props}
    />
  )
})
Card.displayName = "Card"

export { Card }
