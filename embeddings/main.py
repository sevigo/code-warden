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

# --- Core App and Model Configuration ---
app = FastAPI()
model_name = "nomic-ai/nomic-embed-code"

# --- Security Configuration ---
# Read the shared secret from an environment variable for security.
SHARED_SECRET = os.getenv("EMBEDDING_API_SECRET")

# --- Authentication Dependency ---
async def verify_api_key(x_api_key: Optional[str] = Header(None)):
    """Dependency function to verify the incoming API key."""
    # If the secret is not configured on the server, disable authentication.
    if not SHARED_SECRET:
        print("WARNING: EMBEDDING_API_SECRET is not set. Endpoint is open.")
        return

    # If the secret IS configured, we require the header to match.
    if x_api_key is None:
        print("ERROR: Request missing X-Api-Key header.")
        raise HTTPException(status_code=401, detail="X-Api-Key header is required.")
    if x_api_key != SHARED_SECRET:
        print("ERROR: Invalid API Key received.")
        raise HTTPException(status_code=403, detail="Invalid API Key.")

# --- Device Selection (CUDA for NVIDIA GPU on Runpod) ---
print("--- Checking for available devices ---")
if torch.cuda.is_available():
    device = torch.device("cuda")
    print(f"NVIDIA GPU found. Using device: {torch.cuda.get_device_name(0)}")
else:
    device = torch.device("cpu")
    print("NVIDIA GPU not found. Defaulting to CPU.")
print("------------------------------------")

# --- Nomic Model Helper Function (Mean Pooling) ---
def mean_pooling(model_output: Tensor, attention_mask: Tensor) -> Tensor:
    token_embeddings = model_output
    input_mask_expanded = attention_mask.unsqueeze(-1).expand(token_embeddings.size()).float()
    return torch.sum(token_embeddings * input_mask_expanded, 1) / torch.clamp(input_mask_expanded.sum(1), min=1e-9)

# --- Model Loading ---
print(f"\nLoading embedding model '{model_name}'...")
try:
    tokenizer = AutoTokenizer.from_pretrained(model_name, trust_remote_code=True)
    model = AutoModel.from_pretrained(
        model_name,
        trust_remote_code=True,
        torch_dtype=torch.float16
    ).to(device)
    model.eval()
    print(f"Embedding model loaded successfully onto ==> {model.device}")
    print("\n--- API is ready. ---")
except Exception as e:
    print(f"\nFATAL ERROR: Failed to load the embedding model.")
    print(f"Error details: {e}")
    exit()

# --- Pydantic Request Models ---
class EmbedRequest(BaseModel):
    texts: List[str]
    task: str = 'search_document'

# --- API Endpoints ---
# The endpoint is now protected by our verify_api_key dependency.
@app.post("/embed", dependencies=[Depends(verify_api_key)])
async def embed(req: EmbedRequest):
    """
    Generates embeddings for a list of code snippets or text queries.
    This version includes a batching loop to prevent out-of-memory errors.
    """
    try:
        batch_size = 32
        all_embeddings = []
        
        print(f"Received request to embed {len(req.texts)} texts. Processing in batches of {batch_size}...")

        # Process the texts in mini-batches
        for i in range(0, len(req.texts), batch_size):
            batch_texts = req.texts[i:i + batch_size]
            
            print(f"Processing batch { (i // batch_size) + 1 } / { math.ceil(len(req.texts) / batch_size) }...")

            prefixed_texts = [f"{req.task}: {text}" for text in batch_texts]

            encoded_input = tokenizer(
                prefixed_texts,
                padding=True,
                truncation=True,
                max_length=2048,
                return_tensors='pt'
            ).to(device)

            with torch.no_grad():
                model_output = model(**encoded_input).last_hidden_state
                embeddings = mean_pooling(model_output, encoded_input['attention_mask'])
                normalized_embeddings = F.normalize(embeddings, p=2, dim=1)
                
                all_embeddings.append(normalized_embeddings.cpu())

        final_embeddings = torch.cat(all_embeddings, dim=0)

        print("Successfully processed all batches.")
        return {"embeddings": final_embeddings.tolist()}

    except Exception as e:
        print("\n--- AN ERROR OCCURRED IN THE /embed ENDPOINT ---")
        print(f"Error Type: {type(e).__name__}")
        print(f"Error Details: {e}")
        traceback.print_exc()
        print("--------------------------------------------------\n")
        raise HTTPException(status_code=500, detail="Internal Server Error during embedding generation.")