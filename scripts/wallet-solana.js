import {address,appendTransactionMessageInstructions,compileTransaction,createNoopSigner,createTransactionMessage,getBase58Decoder,getTransactionEncoder,pipe,setTransactionMessageFeePayer,setTransactionMessageLifetimeUsingBlockhash} from '@solana/kit';
import {findAssociatedTokenPda,getCreateAssociatedTokenIdempotentInstruction,getTransferCheckedInstruction,TOKEN_PROGRAM_ADDRESS} from '@solana-program/token';
import {getWallets} from '@wallet-standard/app';

export function solanaWallets(){return getWallets().get().filter(wallet=>wallet.chains.includes('solana:mainnet') && wallet.features['standard:connect'] && wallet.features['solana:signAndSendTransaction']);}
export async function buildSolanaPayment(quote,account,lifetime){
 const owner=address(account),mint=address(quote.token_contract),recipient=address(quote.recipient),payer=createNoopSigner(owner);
 const [source]=await findAssociatedTokenPda({owner,mint,tokenProgram:TOKEN_PROGRAM_ADDRESS});
 const [destination]=await findAssociatedTokenPda({owner:recipient,mint,tokenProgram:TOKEN_PROGRAM_ADDRESS});
 const instructions=[getCreateAssociatedTokenIdempotentInstruction({payer,ata:destination,owner:recipient,mint}),getTransferCheckedInstruction({source,mint,destination,authority:payer,amount:BigInt(quote.remaining_units),decimals:quote.decimals})];
 const message=pipe(createTransactionMessage({version:'legacy'}),m=>setTransactionMessageFeePayer(owner,m),m=>setTransactionMessageLifetimeUsingBlockhash({blockhash:lifetime.blockhash,lastValidBlockHeight:BigInt(lifetime.last_valid_block_height)},m),m=>appendTransactionMessageInstructions(instructions,m));
 return getTransactionEncoder().encode(compileTransaction(message));
}
export async function paySolana(quote,walletName,getLifetime){
 const wallets=solanaWallets(),wallet=wallets.find(w=>w.name===walletName) || wallets[0];
 if(!wallet)throw new Error('wallet_unavailable');
 const connected=await wallet.features['standard:connect'].connect();
 const account=connected.accounts.find(a=>a.chains.includes('solana:mainnet'));
 if(!account)throw new Error('wrong_network');
 const lifetime=await getLifetime(account.address);
 const transaction=await buildSolanaPayment(quote,account.address,lifetime);
 const [result]=await wallet.features['solana:signAndSendTransaction'].signAndSendTransaction({account,chain:'solana:mainnet',transaction,options:{commitment:'confirmed'}});
 return getBase58Decoder().decode(result.signature);
}
