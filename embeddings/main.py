import torch
import torch.nn.functional as F
from fastapi import FastAPI, HTTPException, Depends, Header
from pydantic import BaseModel
from transformers import AutoTokenizer, AutoModel
from torch import Tensor
from typing import List, Optional
import traceback
import math
import os
import logging
from contextlib import asynccontextmanager

# --- Logging and Configuration ---
logging.basicConfig(level=logging.INFO, format='%(asctime)s - %(name)s - %(levelname)s - %(message)s')
logger = logging.getLogger(__name__)

MODEL_NAME = os.getenv("MODEL_NAME", "nomic-ai/nomic-embed-code")
BATCH_SIZE = int(os.getenv("BATCH_SIZE", "4"))
MAX_LENGTH = int(os.getenv("MAX_LENGTH", "2048"))
MAX_TEXTS_PER_REQUEST = int(os.getenv("MAX_TEXTS_PER_REQUEST", "1000"))
SHARED_SECRET = os.getenv("EMBEDDING_API_SECRET")

# --- Global State ---
tokenizer = None
model = None
device = None

# --- FastAPI Lifespan (Model Loading) ---
@asynccontextmanager
async def lifespan(app: FastAPI):
    global tokenizer, model, device
    logger.info("--- Checking for available devices ---")
    if torch.cuda.is_available():
        device = torch.device("cuda")
        logger.info(f"NVIDIA GPU found. Using device: {torch.cuda.get_device_name(0)}")
    else:
        device = torch.device("cpu")
        logger.info("NVIDIA GPU not found. Defaulting to CPU.")
    
    logger.info(f"Loading embedding model '{MODEL_NAME}'...")
    try:
        tokenizer = AutoTokenizer.from_pretrained(MODEL_NAME, trust_remote_code=True)
        model = AutoModel.from_pretrained(
            MODEL_NAME,
            trust_remote_code=True,
            torch_dtype=torch.float16
        ).to(device)
        model.eval()
        logger.info(f"Embedding model loaded successfully onto ==> {model.device}")
    except Exception as e:
        logger.error(f"FATAL ERROR during model loading: {e}", exc_info=True)
        raise
    
    logger.info("--- API is ready ---")
    yield
    # Shutdown
    logger.info("Shutting down and clearing cache...")
    if device and device.type == 'cuda':
        torch.cuda.empty_cache()

app = FastAPI(
    title="Code Embedding API", 
    description="High-performance code embedding service with memory management",
    version="1.0.0", 
    lifespan=lifespan
)

# --- Authentication ---
async def verify_api_key(x_api_key: Optional[str] = Header(None)):
    if not SHARED_SECRET:
        logger.warning("EMBEDDING_API_SECRET is not set. Endpoint is open.")
        return
    
    if x_api_key is None:
        logger.error("Request missing X-Api-Key header")
        raise HTTPException(status_code=401, detail="X-Api-Key header is required")
    
    if x_api_key != SHARED_SECRET:
        logger.error("Invalid API Key received")
        raise HTTPException(status_code=403, detail="Invalid API Key")

# --- Helper Functions ---
def mean_pooling(model_output: Tensor, attention_mask: Tensor) -> Tensor:
    """Mean pooling for nomic embeddings."""
    token_embeddings = model_output
    input_mask_expanded = attention_mask.unsqueeze(-1).expand(token_embeddings.size()).float()
    return torch.sum(token_embeddings * input_mask_expanded, 1) / torch.clamp(
        input_mask_expanded.sum(1), min=1e-9
    )

def validate_texts(texts: List[str]) -> None:
    """Validate input texts."""
    if not texts:
        raise HTTPException(status_code=400, detail="texts list cannot be empty")
    
    if len(texts) > MAX_TEXTS_PER_REQUEST:
        raise HTTPException(
            status_code=400, 
            detail=f"Too many texts. Maximum allowed: {MAX_TEXTS_PER_REQUEST}"
        )
    
    for i, text in enumerate(texts):
        if not isinstance(text, str):
            raise HTTPException(
                status_code=400, 
                detail=f"Text at index {i} is not a string"
            )
        if len(text.strip()) == 0:
            raise HTTPException(
                status_code=400, 
                detail=f"Text at index {i} is empty or whitespace only"
            )

# --- Pydantic Models ---
class EmbedRequest(BaseModel):
    texts: List[str]
    task: str = 'search_document'

class EmbedResponse(BaseModel):
    embeddings: List[List[float]]
    count: int
    model: str

class HealthResponse(BaseModel):
    status: str
    model_loaded: bool
    device: str
    batch_size: int

# --- API Endpoints ---
@app.get("/health", response_model=HealthResponse)
async def health_check():
    """Health check endpoint."""
    return HealthResponse(
        status="healthy" if model is not None else "loading",
        model_loaded=model is not None,
        device=str(device) if device else "unknown",
        batch_size=BATCH_SIZE
    )

@app.post("/embed", response_model=EmbedResponse, dependencies=[Depends(verify_api_key)])
async def embed(req: EmbedRequest):
    """
    Generates embeddings for a list of code snippets or text queries.
    Includes memory management and OOM recovery.
    """
    if model is None or tokenizer is None:
        raise HTTPException(status_code=503, detail="Model is not loaded or is warming up")
    
    try:
        # Validate inputs
        validate_texts(req.texts)
        
        all_embeddings = []
        total_batches = math.ceil(len(req.texts) / BATCH_SIZE)
        logger.info(f"Processing {len(req.texts)} texts in {total_batches} batches of size {BATCH_SIZE}")
        
        for i in range(0, len(req.texts), BATCH_SIZE):
            batch_texts = req.texts[i:i + BATCH_SIZE]
            batch_num = (i // BATCH_SIZE) + 1
            logger.info(f"Processing batch {batch_num}/{total_batches} ({len(batch_texts)} texts)")

            prefixed_texts = [f"{req.task}: {text}" for text in batch_texts]

            try:
                # Normal batch processing
                encoded_input = tokenizer(
                    prefixed_texts, 
                    padding=True, 
                    truncation=True, 
                    max_length=MAX_LENGTH, 
                    return_tensors='pt'
                ).to(device)

                with torch.no_grad():
                    model_output_obj = model(**encoded_input)
                    last_hidden_state = model_output_obj.last_hidden_state
                    embeddings = mean_pooling(last_hidden_state, encoded_input['attention_mask'])
                    normalized_embeddings = F.normalize(embeddings, p=2, dim=1)
                    all_embeddings.append(normalized_embeddings.cpu())

                    # Clean up batch tensors immediately
                    del model_output_obj, last_hidden_state, embeddings, normalized_embeddings, encoded_input

            except torch.cuda.OutOfMemoryError as oom_error:
                logger.warning(f"CUDA OOM in batch {batch_num}: {oom_error}")
                logger.info(f"Falling back to individual processing for batch {batch_num}")
                
                # Clear GPU memory
                if device.type == 'cuda':
                    torch.cuda.empty_cache()
                
                # Process one text at a time as fallback
                batch_embeddings = []
                for j, single_text in enumerate(batch_texts):
                    try:
                        prefixed_text = f"{req.task}: {single_text}"
                        encoded_single = tokenizer(
                            [prefixed_text], 
                            padding=True, 
                            truncation=True, 
                            max_length=MAX_LENGTH, 
                            return_tensors='pt'
                        ).to(device)
                        
                        with torch.no_grad():
                            single_output = model(**encoded_single)
                            single_embedding = mean_pooling(
                                single_output.last_hidden_state, 
                                encoded_single['attention_mask']
                            )
                            single_normalized = F.normalize(single_embedding, p=2, dim=1)
                            batch_embeddings.append(single_normalized.cpu())
                        
                        # Clean up individual tensors
                        del single_output, single_embedding, single_normalized, encoded_single
                        
                        if device.type == 'cuda':
                            torch.cuda.empty_cache()
                            
                    except Exception as single_error:
                        logger.error(f"Failed to process individual text {j} in batch {batch_num}: {single_error}")
                        raise HTTPException(
                            status_code=500, 
                            detail=f"Failed to process text at position {i + j}"
                        )
                
                # Combine individual embeddings for this batch
                if batch_embeddings:
                    combined_batch = torch.cat(batch_embeddings, dim=0)
                    all_embeddings.append(combined_batch)
                    del batch_embeddings, combined_batch

        # Concatenate all embeddings
        final_embeddings = torch.cat(all_embeddings, dim=0)
        embeddings_list = final_embeddings.tolist()
        
        # Clean up final variables
        del all_embeddings, final_embeddings
        
        logger.info(f"Successfully generated {len(embeddings_list)} embeddings")
        
        return EmbedResponse(
            embeddings=embeddings_list,
            count=len(embeddings_list),
            model=MODEL_NAME
        )

    except HTTPException:
        # Re-raise HTTP exceptions as-is
        raise
    except Exception as e:
        logger.error(f"Error in /embed endpoint: {type(e).__name__}: {e}", exc_info=True)
        raise HTTPException(
            status_code=500, 
            detail="Internal server error during embedding generation"
        )
    
    finally:
        # Always clear CUDA cache after request
        if device and device.type == 'cuda':
            torch.cuda.empty_cache()
            logger.debug("CUDA cache cleared after request")

@app.get("/")
async def root():
    """Root endpoint with basic API information."""
    return {
        "message": "Code Embedding API",
        "model": MODEL_NAME,
        "batch_size": BATCH_SIZE,
        "endpoints": {
            "health": "/health",
            "embed": "/embed",
            "docs": "/docs"
        }
    }

# --- Global Exception Handler ---
@app.exception_handler(Exception)
async def global_exception_handler(request, exc):
    logger.error(f"Unhandled exception: {type(exc).__name__}: {exc}", exc_info=True)
    # Clear cache on any unhandled exception
    if device and device.type == 'cuda':
        torch.cuda.empty_cache()
    raise HTTPException(status_code=500, detail="Internal server error")

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(
        app,
        host="0.0.0.0",
        port=int(os.getenv("PORT", "18000")),
        log_level="info"
    )